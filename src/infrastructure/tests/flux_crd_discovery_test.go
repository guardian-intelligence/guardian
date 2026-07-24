package tests

import "testing"

func TestFluxCRDDiscoveryEpoch(t *testing.T) {
	const resource = "flux-crd-discovery-refresh.yaml"

	inventory := singleYAMLDoc(t, runfilePath("src/infrastructure/base/app-patches/kustomization.yaml"))
	if !containsString(nestedStringSlice(t, inventory, "resources"), resource) {
		t.Fatalf("app-patches inventory is missing %s", resource)
	}

	patch := singleYAMLDoc(t, runfilePath("src/infrastructure/base/app-patches/"+resource))
	assertNestedString(t, patch, "apps/v1", "apiVersion")
	assertNestedString(t, patch, "Deployment", "kind")
	assertNestedString(t, patch, "flux", "metadata", "name")
	assertNestedString(t, patch, "cozy-fluxcd", "metadata", "namespace")
	assertNestedString(t, patch, "disabled", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/prune")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")
	assertNestedString(t, patch, "flux", "spec", "selector", "matchLabels", "app.kubernetes.io/name")
	assertNestedString(t, patch, "flux", "spec", "template", "metadata", "labels", "app.kubernetes.io/name")
	assertNestedString(
		t,
		patch,
		"external-secrets-v1",
		"spec",
		"template",
		"metadata",
		"annotations",
		"guardian.dev/api-discovery-epoch",
	)
}
