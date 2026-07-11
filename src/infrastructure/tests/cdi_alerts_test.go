package tests

import "testing"

func TestCDIAlertsPreserveGroupAndUseDeliverableSeverities(t *testing.T) {
	sourcePath := runfilePath("src/infrastructure/base/app-patches/cozy-kubevirt-cdi-prometheus-source.yaml")
	source := singleYAMLDoc(t, sourcePath)
	assertNestedString(t, source, "PrometheusRule", "kind")
	assertNestedString(t, source, "prometheus-cdi-rules", "metadata", "name")
	assertNestedString(t, source, "cozy-kubevirt-cdi", "metadata", "namespace")
	assertNestedString(t, source, "Merge", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")
	if groups := mapValue(nestedValue(t, source, "spec"))["groups"]; groups != nil {
		t.Fatal("operator-owned PrometheusRule must not declare rule fields")
	}

	path := runfilePath("src/infrastructure/base/app-patches/cozy-kubevirt-cdi-alerts.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "VMRule", "kind")
	assertNestedString(t, patch, "prometheus-cdi-rules", "metadata", "name")
	assertNestedString(t, patch, "cozy-kubevirt-cdi", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")
	assertNestedString(t, patch, "enabled", "metadata", "annotations", "operator.victoriametrics.com/ignore-prometheus-updates")

	groups := sliceValue(nestedValue(t, patch, "spec", "groups"))
	if len(groups) != 2 {
		t.Fatalf("spec.groups has %d entries, want complete CDI recording and alert groups", len(groups))
	}
	groupsByName := make(map[string]map[string]interface{}, len(groups))
	for _, raw := range groups {
		group := mapValue(raw)
		name, ok := group["name"].(string)
		if !ok || groupsByName[name] != nil {
			t.Fatalf("invalid or duplicate VMRule group %q", name)
		}
		groupsByName[name] = group
	}

	recording := groupsByName["recordingRules.rules"]
	if recording == nil {
		t.Fatal("recordingRules.rules group is missing")
	}
	wantRecords := map[string]bool{
		"kubevirt_cdi_clone_pods_high_restart":  true,
		"kubevirt_cdi_import_pods_high_restart": true,
		"kubevirt_cdi_operator_up":              true,
		"kubevirt_cdi_upload_pods_high_restart": true,
	}
	recordingRules := sliceValue(nestedValue(t, recording, "rules"))
	if len(recordingRules) != len(wantRecords) {
		t.Fatalf("recordingRules.rules has %d rules, want all %d upstream rules", len(recordingRules), len(wantRecords))
	}
	for _, raw := range recordingRules {
		record, ok := mapValue(raw)["record"].(string)
		if !ok || !wantRecords[record] {
			t.Fatalf("unexpected CDI recording rule %q", record)
		}
		delete(wantRecords, record)
	}

	group := groupsByName["alerts.rules"]
	if group == nil {
		t.Fatal("alerts.rules group is missing")
	}

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
