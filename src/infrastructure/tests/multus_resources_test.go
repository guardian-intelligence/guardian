package tests

import "testing"

func TestMultusCPUHeadroomPatch(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/cozy-multus-cpu-resources.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "DaemonSet", "kind")
	assertNestedString(t, patch, "cozy-multus", "metadata", "name")
	assertNestedString(t, patch, "cozy-multus", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	containers := sliceValue(nestedValue(t, patch, "spec", "template", "spec", "containers"))
	if len(containers) != 1 {
		t.Fatalf("spec.template.spec.containers has %d entries, want exactly the kube-multus patch", len(containers))
	}
	container := mapValue(containers[0])
	assertNestedString(t, container, "kube-multus", "name")
	assertNestedString(t, container, "500m", "resources", "limits", "cpu")
	resources := nestedMap(t, container, "resources")
	if _, ownsRequests := resources["requests"]; ownsRequests {
		t.Fatal("Multus patch must leave resource requests under chart ownership")
	}
	if limits := mapValue(resources["limits"]); len(limits) != 1 {
		t.Fatalf("Multus patch owns %d limit fields, want only CPU", len(limits))
	}
}
