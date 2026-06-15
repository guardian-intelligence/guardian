package main

import (
	"strings"
	"testing"
)

func TestCertManagerRenderUsesSeedRegistryImages(t *testing.T) {
	sitePath, err := toolPath("_main/src/sites/dev/bootstrap.yaml")
	if err != nil {
		t.Fatalf("locate bootstrap.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	c := componentByName(t, "cert-manager")
	tmpl, err := toolPath("_main/src/infrastructure-components/cert-manager/k8s/cert-manager.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate cert-manager manifest: %v", err)
	}
	c.manifest = tmpl
	images := map[string]string{
		"cert-manager-cainjector":      "registry.guardian.internal/cert-manager-cainjector@sha256:cafe",
		"cert-manager-controller":      "registry.guardian.internal/cert-manager-controller@sha256:cafe",
		"cert-manager-webhook":         "registry.guardian.internal/cert-manager-webhook@sha256:cafe",
		"cert-manager-startupapicheck": "registry.guardian.internal/cert-manager-startupapicheck@sha256:cafe",
		"cert-manager-acmesolver":      "registry.guardian.internal/cert-manager-acmesolver@sha256:cafe",
	}
	rendered, err := renderComponentManifest(c, "", images, site)
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
	if !strings.Contains(out, "enableGatewayAPI: true") {
		t.Error("cert-manager controller config must enable Gateway API support")
	}
}
