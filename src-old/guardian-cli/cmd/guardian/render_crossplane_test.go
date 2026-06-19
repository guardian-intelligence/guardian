package main

import (
	"strings"
	"testing"
)

func TestCrossplaneRenderUsesSeedRegistryImage(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	c := componentByName(t, "crossplane")
	const image = "registry.guardian.internal/crossplane@sha256:deadbeef"
	rendered, err := buildComponentKustomization(kubectl, c, map[string]string{"crossplane": image}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: Namespace",
		"name: crossplane-system",
		"kind: CustomResourceDefinition",
		"kind: Deployment",
		"image: " + image,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("crossplane render missing %q", want)
		}
	}
	if strings.Contains(out, "xpkg.crossplane.io/crossplane/crossplane:v2.3.2") {
		t.Error("crossplane render must not keep the upstream mutable image reference")
	}
	if strings.Contains(out, "registry.guardian.internal/crossplane:bootstrap") {
		t.Error("crossplane kustomize output must not keep bootstrap image placeholders")
	}
}
