package main

import (
	"strings"
	"testing"
)

func TestExternalSecretsRenderUsesSeedRegistryImage(t *testing.T) {
	sitePath, err := toolPath("_main/src/sites/dev/bootstrap.yaml")
	if err != nil {
		t.Fatalf("locate bootstrap.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	c := componentByName(t, "external-secrets")
	tmpl, err := toolPath("_main/src/infrastructure-components/external-secrets/k8s/external-secrets.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate external-secrets manifest: %v", err)
	}
	c.manifest = tmpl
	const image = "registry.guardian.internal/external-secrets@sha256:deadbeef"
	rendered, err := renderComponentManifest(c, image, nil, site)
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: Namespace",
		"name: external-secrets",
		"namespace: external-secrets",
		"external-secrets-webhook.external-secrets.svc",
		"image: " + image,
		"kind: ClusterRole",
		"name: external-secrets-controller",
		"name: externalsecrets.external-secrets.io",
		"name: secretstores.external-secrets.io",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("external-secrets render missing %q", want)
		}
	}
	for _, banned := range []string{
		"namespace: default",
		"external-secrets-webhook.default.svc",
		"ghcr.io/external-secrets/external-secrets",
		"--namespace=observability",
		`namespace: "observability"`,
	} {
		if strings.Contains(out, banned) {
			t.Errorf("external-secrets render must not contain %q", banned)
		}
	}
}
