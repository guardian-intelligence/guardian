package main

import (
	"strings"
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	cfg := drillConfig{
		Kubectl:              "/kubectl",
		Stage:                "root",
		Namespace:            "tenant-guardian",
		MonitoringNamespace:  "tenant-root",
		StatefulSet:          "guardian-openbao",
		PollTimeout:          time.Minute,
		PollInterval:         time.Second,
		PortForwardReadyWait: time.Second,
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}

	cfg.Stage = "prod"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("validateConfig accepted non-root stage")
	}
	cfg.Stage = "root"
	cfg.Namespace = "Tenant-Guardian"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("validateConfig accepted non-DNS namespace")
	}
}

func TestVictoriaMetricsQueries(t *testing.T) {
	cfg := drillConfig{
		Namespace:   "tenant-guardian",
		StatefulSet: "guardian-openbao",
	}
	queries := victoriaMetricsQueries(cfg)
	joined := make([]string, 0, len(queries))
	for _, query := range queries {
		joined = append(joined, query.label+"="+query.query)
	}
	got := strings.Join(joined, "\n")
	for _, want := range []string{
		`OpenBao scrape targets in VictoriaMetrics=sum(up{namespace="tenant-guardian",job="guardian-openbao-metrics"})`,
		`OpenBao unsealed replicas in VictoriaMetrics=sum(vault_core_unsealed{namespace="tenant-guardian",job="guardian-openbao-metrics"})`,
		`OpenBao active leader in VictoriaMetrics=sum(vault_core_active{namespace="tenant-guardian",job="guardian-openbao-metrics"})`,
		`OpenBao audit request metrics in VictoriaMetrics=sum(vault_audit_log_request_count{namespace="tenant-guardian",job="guardian-openbao-metrics"})`,
		`OpenBao audit tailer running in VictoriaMetrics=sum(kube_pod_container_status_running{namespace="tenant-guardian",container="audit-log-tailer",pod=~"guardian-openbao-.*"})`,
		`OpenBao ops controller scrape target in VictoriaMetrics=sum(up{namespace="tenant-guardian",job="openbao-ops-controller"})`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("victoriaMetricsQueries missing %q:\n%s", want, got)
		}
	}
}

func TestVictoriaLogsQueries(t *testing.T) {
	cfg := drillConfig{
		Namespace:   "tenant-guardian",
		StatefulSet: "guardian-openbao",
	}
	queries := victoriaLogsQueries(cfg)
	joined := make([]string, 0, len(queries))
	for _, query := range queries {
		joined = append(joined, query.label+"="+query.query)
	}
	got := strings.Join(joined, "\n")
	for _, want := range []string{
		`OpenBao audit tailer logs in VictoriaLogs=kubernetes_namespace_name:tenant-guardian kubernetes_container_name:audit-log-tailer kubernetes_pod_name:guardian-openbao-*`,
		`OpenBao ops controller logs in VictoriaLogs=kubernetes_namespace_name:tenant-guardian kubernetes_container_name:manager kubernetes_pod_name:openbao-ops-controller-*`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("victoriaLogsQueries missing %q:\n%s", want, got)
		}
	}
}

func TestStatefulSetRolled(t *testing.T) {
	raw := `{
		"spec": {"replicas": 3},
		"status": {
			"readyReplicas": 3,
			"updatedReplicas": 3,
			"currentRevision": "guardian-openbao-new",
			"updateRevision": "guardian-openbao-new"
		}
	}`
	ok, summary, err := statefulSetRolled(raw)
	if err != nil {
		t.Fatalf("statefulSetRolled: %v", err)
	}
	if !ok {
		t.Fatalf("statefulSetRolled ok=false summary=%s", summary)
	}

	raw = strings.Replace(raw, `"updatedReplicas": 3`, `"updatedReplicas": 2`, 1)
	ok, _, err = statefulSetRolled(raw)
	if err != nil {
		t.Fatalf("statefulSetRolled stale: %v", err)
	}
	if ok {
		t.Fatal("statefulSetRolled accepted stale StatefulSet")
	}
}

func TestPrometheusValuePositive(t *testing.T) {
	if !prometheusValuePositive([]any{float64(1), "3"}) {
		t.Fatal("positive Prometheus value was not accepted")
	}
	if prometheusValuePositive([]any{float64(1), "0"}) {
		t.Fatal("zero Prometheus value was accepted")
	}
	if prometheusValuePositive([]any{float64(1), "not-a-number"}) {
		t.Fatal("invalid Prometheus value was accepted")
	}
}
