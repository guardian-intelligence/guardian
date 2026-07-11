package tests

import "testing"

func TestCDIAlertsPreserveGroupAndUseDeliverableSeverities(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/cozy-kubevirt-cdi-alerts.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "PrometheusRule", "kind")
	assertNestedString(t, patch, "prometheus-cdi-rules", "metadata", "name")
	assertNestedString(t, patch, "cozy-kubevirt-cdi", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	groups := sliceValue(nestedValue(t, patch, "spec", "groups"))
	if len(groups) != 1 {
		t.Fatalf("spec.groups has %d entries, want the complete CDI alert group", len(groups))
	}
	group := mapValue(groups[0])
	assertNestedString(t, group, "alerts.rules", "name")

	wantAlerts := map[string]bool{
		"CDIDataImportCronOutdated":            true,
		"CDIDataVolumeUnusualRestartCount":     true,
		"CDIDefaultStorageClassDegraded":       true,
		"CDIMultipleDefaultVirtStorageClasses": true,
		"CDINoDefaultStorageClass":             true,
		"CDINotReady":                          true,
		"CDIOperatorDown":                      true,
		"CDIStorageProfilesIncomplete":         true,
	}
	rules := sliceValue(nestedValue(t, group, "rules"))
	if len(rules) != len(wantAlerts) {
		t.Fatalf("alerts.rules has %d rules, want all %d upstream rules", len(rules), len(wantAlerts))
	}

	seen := make(map[string]bool, len(rules))
	for _, raw := range rules {
		rule := mapValue(raw)
		alert, ok := rule["alert"].(string)
		if !ok || !wantAlerts[alert] || seen[alert] {
			t.Fatalf("unexpected or duplicate CDI alert %q", alert)
		}
		seen[alert] = true
		severity := nestedValue(t, rule, "labels", "severity")
		if severity != "critical" && severity != "warning" && severity != "informational" {
			t.Fatalf("CDI alert %q uses Alerta-incompatible severity %q", alert, severity)
		}
		if alert == "CDIStorageProfilesIncomplete" {
			assertNestedString(t, rule, "informational", "labels", "severity")
			assertNestedString(t, rule, "5m", "for")
		}
	}
}
