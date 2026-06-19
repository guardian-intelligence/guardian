package main

import (
	"strings"
	"testing"
)

func TestCertManagerRenderUsesSeedRegistryImages(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	c := componentByName(t, "cert-manager")
	images := map[string]string{
		"cert-manager-cainjector":      "registry.guardian.internal/cert-manager-cainjector@sha256:cafe",
		"cert-manager-controller":      "registry.guardian.internal/cert-manager-controller@sha256:cafe",
		"cert-manager-webhook":         "registry.guardian.internal/cert-manager-webhook@sha256:cafe",
		"cert-manager-startupapicheck": "registry.guardian.internal/cert-manager-startupapicheck@sha256:cafe",
		"cert-manager-acmesolver":      "registry.guardian.internal/cert-manager-acmesolver@sha256:cafe",
	}
	rendered, err := buildComponentKustomization(kubectl, c, images, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for name, ref := range images {
		if !strings.Contains(out, ref) {
			t.Errorf("rendered cert-manager manifest missing %s ref %s", name, ref)
		}
	}
	if strings.Contains(out, "image: \"quay.io/jetstack/") || strings.Contains(out, "--acme-http01-solver-image=quay.io/jetstack/") {
		t.Error("cert-manager manifest must use seed-registry refs, not upstream quay.io image refs")
	}
	if strings.Contains(out, "registry.guardian.internal/cert-manager-controller:bootstrap") {
		t.Error("cert-manager kustomize output must not keep bootstrap image placeholders")
	}
	if !strings.Contains(out, "enableGatewayAPI: true") {
		t.Error("cert-manager controller config must enable Gateway API support")
	}
	if !strings.Contains(out, "pod-security.kubernetes.io/enforce: privileged") {
		t.Error("cert-manager namespace must allow the hostNetwork controller")
	}
	if !strings.Contains(out, "hostNetwork: true") {
		t.Error("cert-manager controller must run hostNetwork for Gateway API certificate watches")
	}
}
