package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlatformTLSSurvivalSecretRefs(t *testing.T) {
	sitePath, err := toolPath("_main/src/hosts/ash-bm-001/host.yaml")
	if err != nil {
		t.Fatalf("locate host.yaml: %v", err)
	}
	site, err := loadHost(sitePath)
	if err != nil {
		t.Fatalf("load site: %v", err)
	}
	got, err := platformTLSSurvivalSecretRefs(site)
	if err != nil {
		t.Fatalf("platform TLS refs: %v", err)
	}
	want := []platformTLSSecretRef{
		{namespace: "cert-manager", name: "cloudflare-guardianintelligence-org-dns-token"},
		{namespace: "cert-manager", name: "letsencrypt-production-account-key"},
		{namespace: "gateway", name: "aisucks-tls"},
		{namespace: "gateway", name: "oci-guardianintelligence-org-tls"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs = %#v, want %#v", got, want)
	}
}

func TestEdgeGatewayCertificateObjectNames(t *testing.T) {
	sitePath, err := toolPath("_main/src/hosts/ash-bm-001/host.yaml")
	if err != nil {
		t.Fatalf("locate host.yaml: %v", err)
	}
	site, err := loadHost(sitePath)
	if err != nil {
		t.Fatalf("load site: %v", err)
	}
	got, err := edgeGatewayCertificateObjectNames(site)
	if err != nil {
		t.Fatalf("certificate object names: %v", err)
	}
	want := []string{
		"edge-gateway-certificate-aisucks-tls",
		"edge-gateway-certificate-oci-guardianintelligence-org-tls",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
}

func TestEdgeGatewayCertificateRefs(t *testing.T) {
	sitePath, err := toolPath("_main/src/hosts/ash-bm-001/host.yaml")
	if err != nil {
		t.Fatalf("locate host.yaml: %v", err)
	}
	site, err := loadHost(sitePath)
	if err != nil {
		t.Fatalf("load site: %v", err)
	}
	got, err := edgeGatewayCertificateRefs(site)
	if err != nil {
		t.Fatalf("certificate refs: %v", err)
	}
	want := []edgeGatewayCertificateRef{
		{namespace: "gateway", name: "aisucks-tls"},
		{namespace: "gateway", name: "oci-guardianintelligence-org-tls"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs = %#v, want %#v", got, want)
	}
}

func TestEdgeGatewayCertificateTargets(t *testing.T) {
	sitePath, err := toolPath("_main/src/hosts/ash-bm-001/host.yaml")
	if err != nil {
		t.Fatalf("locate host.yaml: %v", err)
	}
	site, err := loadHost(sitePath)
	if err != nil {
		t.Fatalf("load site: %v", err)
	}
	got, err := edgeGatewayCertificateTargets(site)
	if err != nil {
		t.Fatalf("certificate targets: %v", err)
	}
	want := []edgeGatewayCertificateTarget{
		{namespace: "gateway", name: "aisucks-tls", dnsNames: []string{"dev.aisucks.app"}},
		{namespace: "gateway", name: "oci-guardianintelligence-org-tls", dnsNames: []string{"oci.guardianintelligence.org"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %#v, want %#v", got, want)
	}
}

func TestSanitizeSecretBackup(t *testing.T) {
	raw := []byte(`{
	  "apiVersion": "v1",
	  "kind": "Secret",
	  "metadata": {
	    "name": "oci-guardianintelligence-org-tls",
	    "namespace": "gateway",
	    "uid": "drop-me",
	    "resourceVersion": "drop-me",
	    "annotations": {
	      "cert-manager.io/certificate-name": "oci-guardianintelligence-org-tls",
	      "kubectl.kubernetes.io/last-applied-configuration": "drop-me"
	    }
	  },
	  "type": "kubernetes.io/tls",
	  "data": {
	    "tls.crt": "Y2VydA==",
	    "tls.key": "a2V5"
	  }
	}`)
	got, err := sanitizeSecretBackup(raw, platformTLSSecretRef{namespace: "gateway", name: "oci-guardianintelligence-org-tls"})
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if bytes.Contains(got, []byte("resourceVersion")) || bytes.Contains(got, []byte("uid")) || bytes.Contains(got, []byte("last-applied")) {
		t.Fatalf("sanitized secret retained server-side/apply metadata:\n%s", got)
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("sanitized secret JSON: %v", err)
	}
	meta := parsed["metadata"].(map[string]any)
	if meta["name"] != "oci-guardianintelligence-org-tls" || meta["namespace"] != "gateway" {
		t.Fatalf("metadata = %#v", meta)
	}
	annotations := meta["annotations"].(map[string]any)
	if annotations["cert-manager.io/certificate-name"] != "oci-guardianintelligence-org-tls" {
		t.Fatalf("annotations = %#v", annotations)
	}
}

func TestRestorePlatformTLSSecretsSkipsMissingBackups(t *testing.T) {
	sitePath, err := toolPath("_main/src/hosts/ash-bm-001/host.yaml")
	if err != nil {
		t.Fatalf("locate host.yaml: %v", err)
	}
	site, err := loadHost(sitePath)
	if err != nil {
		t.Fatalf("load site: %v", err)
	}
	if err := restorePlatformTLSSecrets("kubectl-would-fail-if-called", "kubeconfig", t.TempDir(), site); err != nil {
		t.Fatalf("restore without backups: %v", err)
	}
}

func TestPlatformTLSSecretBackupPath(t *testing.T) {
	got := platformTLSSecretBackupPath("/state", platformTLSSecretRef{namespace: "gateway", name: "oci"})
	want := filepath.Join("/state", platformTLSStateDir, "gateway", "oci.secret.json")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if strings.Contains(got, "..") {
		t.Fatalf("path contains traversal: %q", got)
	}
}
