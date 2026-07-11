package tests

import (
	"strings"
	"testing"
)

func TestDRBDAlertsMatchFaultDomainsAndPreserveSustainedFailures(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/cozy-linstor-drbd-alerts.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "PrometheusRule", "kind")
	assertNestedString(t, patch, "piraeus-datastore", "metadata", "name")
	assertNestedString(t, patch, "cozy-linstor", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	groups := sliceValue(nestedValue(t, patch, "spec", "groups"))
	if len(groups) != 1 {
		t.Fatalf("spec.groups has %d entries, want the DRBD group", len(groups))
	}
	group := mapValue(groups[0])
	assertNestedString(t, group, "drbd.rules", "name")

	wantAlerts := map[string]bool{
		"drbdReactorOffline":                 true,
		"drbdConnectionNotConnected":         true,
		"drbdDeviceNotUpToDate":              true,
		"drbdDeviceUnintentionalDiskless":    true,
		"drbdDeviceWithoutQuorum":            true,
		"drbdResourceSuspended":              true,
		"drbdResourceResyncWithoutProgress":  true,
		"drbdResourceWithNoUpToDateReplicas": true,
	}
	rules := sliceValue(nestedValue(t, group, "rules"))
	if len(rules) != len(wantAlerts) {
		t.Fatalf("drbd.rules has %d rules, want all %d upstream rules", len(rules), len(wantAlerts))
	}

	seen := make(map[string]bool, len(rules))
	byAlert := make(map[string]map[string]interface{}, len(rules))
	for _, raw := range rules {
		rule := mapValue(raw)
		alert, ok := rule["alert"].(string)
		if !ok || !wantAlerts[alert] {
			t.Fatalf("unexpected DRBD alert %q", alert)
		}
		if seen[alert] {
			t.Fatalf("duplicate DRBD alert %q", alert)
		}
		seen[alert] = true
		byAlert[alert] = rule
	}
	for alert, rule := range byAlert {
		severity := nestedValue(t, rule, "labels", "severity")
		if severity != "critical" && severity != "warning" {
			t.Fatalf("DRBD alert %q uses Alerta-incompatible severity %q", alert, severity)
		}
	}

	connection := byAlert["drbdConnectionNotConnected"]
	assertNestedString(t, connection, "2m", "for")
	assertNestedString(t, connection, "warning", "labels", "severity")
	assertNestedString(t, connection, "{{ $labels.node }}->{{ $labels.conn_name }}", "labels", "exported_instance")
	connectionExpr := nestedValue(t, connection, "expr").(string)
	if !strings.Contains(connectionExpr, "sum by (cluster, tenant, tier, prometheus, job, node, conn_name)") {
		t.Fatalf("connection alert is not aggregated by DRBD link: %s", connectionExpr)
	}

	device := byAlert["drbdDeviceNotUpToDate"]
	assertNestedString(t, device, "2m", "for")
	assertNestedString(t, device, "warning", "labels", "severity")
	assertNestedString(t, device, "{{ $labels.node }}/drbd-devices", "labels", "exported_instance")
	deviceExpr := nestedValue(t, device, "expr").(string)
	if !strings.Contains(deviceExpr, "sum by (cluster, tenant, tier, prometheus, job, node)") {
		t.Fatalf("device alert is not aggregated by node: %s", deviceExpr)
	}

	noReplicas := byAlert["drbdResourceWithNoUpToDateReplicas"]
	assertNestedString(t, noReplicas, "critical", "labels", "severity")
	if _, delayed := noReplicas["for"]; delayed {
		t.Fatal("no-UpToDate-replicas alert must remain immediate")
	}
	stalled := byAlert["drbdResourceResyncWithoutProgress"]
	if expr := nestedValue(t, stalled, "expr").(string); !strings.Contains(expr, "delta(drbd_peerdevice_outofsync_bytes[5m]) >= 0") {
		t.Fatalf("stalled-resync alert lost its five-minute progress test: %s", expr)
	}
}
