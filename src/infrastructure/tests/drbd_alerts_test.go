package tests

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDRBDAlertsMatchFaultDomainsAndPreserveSustainedFailures(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/cozy-linstor-drbd-alerts.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "HelmRelease", "kind")
	assertNestedString(t, patch, "piraeus-operator", "metadata", "name")
	assertNestedString(t, patch, "cozy-linstor", "metadata", "namespace")

	postRenderers := sliceValue(nestedValue(t, patch, "spec", "postRenderers"))
	if len(postRenderers) != 1 {
		t.Fatalf("spec.postRenderers has %d entries, want one", len(postRenderers))
	}
	patches := sliceValue(nestedValue(t, mapValue(postRenderers[0]), "kustomize", "patches"))
	if len(patches) != 1 {
		t.Fatalf("post-renderer has %d patches, want one", len(patches))
	}
	rulePatch := mapValue(patches[0])
	assertNestedString(t, rulePatch, "monitoring.coreos.com", "target", "group")
	assertNestedString(t, rulePatch, "v1", "target", "version")
	assertNestedString(t, rulePatch, "PrometheusRule", "target", "kind")
	assertNestedString(t, rulePatch, "piraeus-datastore", "target", "name")

	ops, ok := rulePatch["patch"].(string)
	if !ok {
		t.Fatal("post-renderer rule patch is not a string")
	}
	var operations []map[string]interface{}
	if err := yaml.Unmarshal([]byte(ops), &operations); err != nil {
		t.Fatalf("parse DRBD post-renderer operations: %v", err)
	}
	byPath := make(map[string]map[string]interface{}, len(operations))
	for _, operation := range operations {
		path, ok := operation["path"].(string)
		if !ok {
			t.Fatalf("DRBD post-renderer operation has no string path: %#v", operation)
		}
		if _, exists := byPath[path]; exists {
			t.Fatalf("DRBD post-renderer repeats path %q", path)
		}
		byPath[path] = operation
	}
	assertOperation := func(path, operation, value string) {
		t.Helper()
		got, ok := byPath[path]
		if !ok {
			t.Fatalf("DRBD post-renderer is missing path %q", path)
		}
		if got["op"] != operation || got["value"] != value {
			t.Fatalf("DRBD post-renderer path %q = %#v, want op=%q value=%q", path, got, operation, value)
		}
	}
	assertOperation("/spec/groups/1/name", "test", "drbd.rules")
	assertOperation("/spec/groups/1/rules/1/alert", "test", "drbdConnectionNotConnected")
	assertOperation("/spec/groups/1/rules/2/alert", "test", "drbdDeviceNotUpToDate")
	assertOperation("/spec/groups/1/rules/1/for", "add", "2m")
	assertOperation("/spec/groups/1/rules/2/for", "add", "2m")
	assertOperation("/spec/groups/1/rules/1/labels/exported_instance", "add", "{{ $labels.node }}->{{ $labels.conn_name }}")
	assertOperation("/spec/groups/1/rules/2/labels/exported_instance", "add", "{{ $labels.node }}/drbd-devices")
	assertOperation("/spec/groups/1/rules/1/labels/severity", "replace", "warning")
	assertOperation("/spec/groups/1/rules/2/labels/severity", "replace", "warning")
	assertOperation("/spec/groups/1/rules/1/expr", "replace", `sum by (cluster, tenant, tier, prometheus, job, node, conn_name) (drbd_connection_state{drbd_connection_state!="Connected"} > 0) > 0`)
	assertOperation("/spec/groups/1/rules/2/expr", "replace", `sum by (cluster, tenant, tier, prometheus, job, node) (drbd_device_state{drbd_device_state!~"UpToDate|Diskless"} > 0) > 0`)
	if strings.Contains(ops, "drbdResourceWithNoUpToDateReplicas") || strings.Contains(ops, "drbdResourceResyncWithoutProgress") {
		t.Fatal("DRBD post-renderer must not delay immediate data-loss or stalled-resync alerts")
	}
}
