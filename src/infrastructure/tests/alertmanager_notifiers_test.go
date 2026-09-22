package tests

import (
	"fmt"
	"strings"
	"testing"
)

// Alertmanager replicas do not share alerts, so each evaluator must notify
// every replica by its own pod DNS name. A Service name lets keep-alive pin
// one replica, and a replica switch then resolves and re-fires every open
// page.
func TestEvaluatorsNotifyEveryAlertmanagerReplica(t *testing.T) {
	const replicas = 3
	var want []string
	for i := 0; i < replicas; i++ {
		want = append(want, fmt.Sprintf(
			"http://vmalertmanager-alertmanager-%d.vmalertmanager-alertmanager.tenant-root.svc:9093", i))
	}

	shorttermPath := runfilePath("src/infrastructure/base/app-patches/monitoring-vmalert-notifiers.yaml")
	shortterm := singleYAMLDoc(t, shorttermPath)
	assertNestedString(t, shortterm, "VMAlert", "kind")
	assertNestedString(t, shortterm, "vmalert-shortterm", "metadata", "name")
	assertNestedString(t, shortterm, "tenant-root", "metadata", "namespace")
	assertNestedString(t, shortterm, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	vlogsPath := runfilePath("src/infrastructure/deployments/alerting/vmalert-vlogs.yaml")
	vlogs := findDoc(t, yamlDocs(t, vlogsPath), "VMAlert", "vmalert-vlogs")

	for path, vmalert := range map[string]map[string]interface{}{shorttermPath: shortterm, vlogsPath: vlogs} {
		var got []string
		for _, raw := range sliceValue(nestedValue(t, vmalert, "spec", "notifiers")) {
			got = append(got, stringValue(mapValue(raw)["url"]))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: notifiers = %q, want every replica %q", path, got, want)
		}
	}

	kustomizationPath := runfilePath("src/infrastructure/base/app-patches/kustomization.yaml")
	listed := false
	for _, resource := range sliceValue(nestedValue(t, singleYAMLDoc(t, kustomizationPath), "resources")) {
		if stringValue(resource) == "monitoring-vmalert-notifiers.yaml" {
			listed = true
		}
	}
	if !listed {
		t.Errorf("%s does not apply monitoring-vmalert-notifiers.yaml", kustomizationPath)
	}
}
