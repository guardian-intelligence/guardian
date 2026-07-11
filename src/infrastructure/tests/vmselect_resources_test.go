package tests

import "testing"

func TestShortTermVMSelectOwnedResources(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/monitoring-shortterm-vmselect-resources.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "VMCluster", "kind")
	assertNestedString(t, patch, "shortterm", "metadata", "name")
	assertNestedString(t, patch, "tenant-root", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")
	assertNestedString(t, patch, "500m", "spec", "vmselect", "resources", "requests", "cpu")
	assertNestedString(t, patch, "500Mi", "spec", "vmselect", "resources", "requests", "memory")
	assertNestedString(t, patch, "2", "spec", "vmselect", "resources", "limits", "cpu")
	assertNestedString(t, patch, "1000Mi", "spec", "vmselect", "resources", "limits", "memory")

	spec := nestedMap(t, patch, "spec")
	if len(spec) != 1 {
		t.Fatalf("VMCluster patch owns %d spec fields, want only vmselect", len(spec))
	}
	vmselect := nestedMap(t, patch, "spec", "vmselect")
	if len(vmselect) != 1 {
		t.Fatalf("VMCluster patch owns %d vmselect fields, want only resources", len(vmselect))
	}
	resources := nestedMap(t, patch, "spec", "vmselect", "resources")
	if len(nestedMap(t, resources, "requests")) != 2 || len(nestedMap(t, resources, "limits")) != 2 {
		t.Fatal("VMCluster patch must own complete CPU and memory requests and limits")
	}
}
