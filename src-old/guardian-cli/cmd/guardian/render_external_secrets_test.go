package main

import (
	"strings"
	"testing"
)

func TestExternalSecretsRenderUsesSeedRegistryImage(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	c := componentByName(t, "external-secrets")
	const image = "registry.guardian.internal/external-secrets@sha256:deadbeef"
	rendered, err := buildComponentKustomization(kubectl, c, map[string]string{"external-secrets": image}, nil)
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
		"registry.guardian.internal/external-secrets:bootstrap",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("external-secrets render must not contain %q", banned)
		}
	}
}
