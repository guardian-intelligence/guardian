package tests

import (
	"strings"
	"testing"
)

func TestKubeClientCertificateExpirationMatchesShortLivedClients(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/monitoring-agents-kube-client-certificate-expiration.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "VMRule", "kind")
	assertNestedString(t, patch, "alerts-kubernetes-system-apiserver", "metadata", "name")
	assertNestedString(t, patch, "cozy-monitoring", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	groups := sliceValue(nestedValue(t, patch, "spec", "groups"))
	if len(groups) != 1 {
		t.Fatalf("spec.groups has %d entries, want the complete kubernetes-system-apiserver group", len(groups))
	}
	group := mapValue(groups[0])
	assertNestedString(t, group, "kubernetes-system-apiserver", "name")

	wantAlerts := map[string]int{
		"KubeClientCertificateExpiration": 1,
		"KubeAggregatedAPIErrors":         1,
		"KubeAggregatedAPIDown":           1,
		"KubeAPIDown":                     1,
		"KubeAPITerminatedRequests":       1,
	}
	var certificateRule map[string]interface{}
	for _, raw := range sliceValue(nestedValue(t, group, "rules")) {
		rule := mapValue(raw)
		alert, ok := rule["alert"].(string)
		if !ok || wantAlerts[alert] == 0 {
			t.Fatalf("unexpected kubernetes-system-apiserver alert %q", alert)
		}
		wantAlerts[alert]--
		if alert == "KubeClientCertificateExpiration" {
			certificateRule = rule
		}
	}
	for alert, remaining := range wantAlerts {
		if remaining != 0 {
			t.Fatalf("alert %s appears %d fewer times than required", alert, remaining)
		}
	}
	if certificateRule == nil {
		t.Fatal("KubeClientCertificateExpiration rule is missing")
	}
	assertNestedString(t, certificateRule, "15m", "for")
	assertNestedString(t, certificateRule, "warning", "labels", "severity")
	assertNestedString(t, certificateRule, "{{ $labels.cluster }}/apiserver", "labels", "exported_instance")
	expr := stringValue(certificateRule["expr"])
	for _, want := range []string{"histogram_quantile(0.01", "< 3600"} {
		if !strings.Contains(expr, want) {
			t.Fatalf("KubeClientCertificateExpiration expression is missing %q: %s", want, expr)
		}
	}
	for _, oldThreshold := range []string{"604800", "86400"} {
		if strings.Contains(expr, oldThreshold) {
			t.Fatalf("KubeClientCertificateExpiration retains incompatible threshold %s: %s", oldThreshold, expr)
		}
	}
}

func TestPGMetricsAbsentRequiresCurrentCNPGInstance(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/alerting/postgres-alerts.yaml")
	ruleSet := singleYAMLDoc(t, path)
	groups := sliceValue(nestedValue(t, ruleSet, "spec", "groups"))
	var absentRule map[string]interface{}
	for _, rawGroup := range groups {
		for _, rawRule := range sliceValue(mapValue(rawGroup)["rules"]) {
			rule := mapValue(rawRule)
			if rule["alert"] == "PGMetricsAbsent" {
				absentRule = rule
			}
		}
	}
	if absentRule == nil {
		t.Fatal("PGMetricsAbsent rule is missing")
	}
	expr := stringValue(absentRule["expr"])
	for _, want := range []string{
		"offset 1h",
		"and on (namespace, job)",
		"label_join(",
		`kube_pod_labels{label_cnpg_io_pod_role="instance"}`,
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("PGMetricsAbsent expression is missing %q: %s", want, expr)
		}
	}
}

func TestEtcdFragmentationRequiresMaterialReclaimableSpace(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/monitoring-agents-etcd-alerts.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "VMRule", "kind")
	assertNestedString(t, patch, "alerts-etcd", "metadata", "name")
	assertNestedString(t, patch, "cozy-monitoring", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	groups := sliceValue(nestedValue(t, patch, "spec", "groups"))
	if len(groups) != 1 {
		t.Fatalf("spec.groups has %d entries, want the complete etcd group", len(groups))
	}
	group := mapValue(groups[0])
	assertNestedString(t, group, "etcd", "name")

	wantAlerts := map[string]int{
		"etcdMembersDown":                    1,
		"etcdInsufficientMembers":            1,
		"etcdNoLeader":                       1,
		"etcdHighNumberOfLeaderChanges":      1,
		"etcdHighNumberOfFailedGRPCRequests": 2,
		"etcdGRPCRequestsSlow":               1,
		"etcdMemberCommunicationSlow":        1,
		"etcdHighNumberOfFailedProposals":    1,
		"etcdHighFsyncDurations":             2,
		"etcdHighCommitDurations":            1,
		"etcdDatabaseQuotaLowSpace":          1,
		"etcdExcessiveDatabaseGrowth":        1,
		"etcdDatabaseHighFragmentationRatio": 1,
	}
	var fragmentationRule map[string]interface{}
	for _, raw := range sliceValue(nestedValue(t, group, "rules")) {
		rule := mapValue(raw)
		alert, ok := rule["alert"].(string)
		if !ok || wantAlerts[alert] == 0 {
			t.Fatalf("unexpected etcd alert %q", alert)
		}
		wantAlerts[alert]--
		if alert == "etcdDatabaseHighFragmentationRatio" {
			fragmentationRule = rule
		}
	}
	for alert, remaining := range wantAlerts {
		if remaining != 0 {
			t.Fatalf("alert %s appears %d fewer times than required", alert, remaining)
		}
	}
	if fragmentationRule == nil {
		t.Fatal("etcdDatabaseHighFragmentationRatio rule is missing")
	}
	assertNestedString(t, fragmentationRule, "10m", "for")
	expr := stringValue(fragmentationRule["expr"])
	for _, want := range []string{
		"etcd_mvcc_db_total_size_in_use_in_bytes",
		"etcd_mvcc_db_total_size_in_bytes",
		"etcd_server_quota_backend_bytes",
		"< 0.5",
		"> 0.05",
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("etcdDatabaseHighFragmentationRatio expression is missing %q: %s", want, expr)
		}
	}
	if strings.Contains(expr, "> 104857600") {
		t.Fatalf("etcdDatabaseHighFragmentationRatio retains the fixed 100 MiB floor: %s", expr)
	}
}
