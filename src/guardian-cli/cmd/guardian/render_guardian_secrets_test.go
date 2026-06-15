package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGuardianSecretsRenderProjectsObservabilitySecrets(t *testing.T) {
	sitePath, err := toolPath("_main/src/sites/dev/bootstrap.yaml")
	if err != nil {
		t.Fatalf("locate bootstrap.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	c := componentByName(t, "guardian-secrets")
	tmpl, err := toolPath("_main/src/infrastructure-components/guardian-secrets/k8s/guardian-secrets.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate guardian-secrets manifest: %v", err)
	}
	c.manifest = tmpl
	rendered, err := renderComponentManifest(c, "", nil, site)
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	decodeKinds(t, rendered)
	for _, want := range []string{
		"kind: ServiceAccount",
		"name: external-secrets-observability",
		"kind: SecretStore",
		"name: openbao",
		"server: http://openbao.openbao.svc:8200",
		"path: kv",
		"version: v2",
		"mountPath: kubernetes",
		"role: observability-secrets",
		"kind: ExternalSecret",
		"name: clickhouse-admin",
		"name: grafana-admin",
		"namespace: observability",
		"key: guardian/" + site.Cluster.Name + "/observability/clickhouse-admin",
		"key: guardian/" + site.Cluster.Name + "/observability/grafana-admin",
		"property: password",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guardian-secrets render missing %q", want)
		}
	}
	if strings.Contains(out, "tokenSecretRef") {
		t.Error("guardian-secrets must use Kubernetes auth, not a static OpenBao token Secret")
	}
	if strings.Contains(out, "ClusterSecretStore") {
		t.Error("observability projection should stay namespace-scoped through SecretStore")
	}
	if strings.Contains(out, "guardian-oci") || strings.Contains(out, "zot-publisher") {
		t.Error("guardian-secrets should only render observability projections; zot is Crossplane-managed")
	}
	assertExternalSecretData(t, rendered, "clickhouse-admin", "grafana-admin")
}

func assertExternalSecretData(t *testing.T, manifest []byte, names ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, name := range names {
		want[name] = false
	}
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Data []struct {
					SecretKey string `yaml:"secretKey"`
				} `yaml:"data"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode guardian-secrets manifest: %v", err)
		}
		if doc.Kind != "ExternalSecret" {
			continue
		}
		if _, ok := want[doc.Metadata.Name]; !ok {
			continue
		}
		if len(doc.Spec.Data) == 0 {
			t.Fatalf("ExternalSecret %s has no spec.data entries", doc.Metadata.Name)
		}
		want[doc.Metadata.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Fatalf("ExternalSecret %s not found", name)
		}
	}
}
