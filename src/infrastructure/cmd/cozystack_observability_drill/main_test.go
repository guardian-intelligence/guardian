package main

import (
	"strings"
	"testing"
	"time"
)

func TestNamespaceForStage(t *testing.T) {
	got, err := namespaceForStage("root")
	if err != nil {
		t.Fatalf("namespaceForStage(root): %v", err)
	}
	if got != "tenant-root" {
		t.Fatalf("namespaceForStage(root) = %q, want tenant-root", got)
	}
	if _, err := namespaceForStage("prod"); err == nil {
		t.Fatal("namespaceForStage(prod) succeeded; want root-only error")
	}
}

func TestDefaultJobName(t *testing.T) {
	got := defaultJobName("root", time.Date(2026, 6, 24, 3, 4, 5, 0, time.UTC))
	if got != "guardian-root-observability-20260624t030405z" {
		t.Fatalf("defaultJobName = %q", got)
	}
}

func TestValidateConfig(t *testing.T) {
	cfg := observabilityConfig{
		Kubectl:                 "/kubectl",
		Stage:                   "root",
		Namespace:               "tenant-root",
		ApplicationName:         "guardian",
		Name:                    "guardian-root-observability-20260624t030405z",
		TTLSecondsAfterFinished: "86400",
		PgbenchScale:            "10",
		PgbenchClients:          "4",
		PgbenchJobs:             "2",
		PgbenchDurationSeconds:  "30",
		PollTimeout:             time.Minute,
		PollInterval:            time.Second,
		PortForwardReadyWait:    time.Second,
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}

	cfg.ApplicationName = "Guardian"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("validateConfig accepted non-DNS application")
	}
}

func TestPostgresJobManifest(t *testing.T) {
	cfg := observabilityConfig{
		Stage:                   "root",
		Namespace:               "tenant-root",
		ApplicationName:         "guardian",
		Name:                    "guardian-root-observability-20260624t030405z",
		TTLSecondsAfterFinished: "86400",
		PgbenchScale:            "10",
		PgbenchClients:          "4",
		PgbenchJobs:             "2",
		PgbenchDurationSeconds:  "30",
	}
	got := postgresJobManifest(cfg)
	for _, want := range []string{
		"kind: Job\nmetadata:\n  name: guardian-root-observability-20260624t030405z\n  namespace: tenant-root\n",
		"guardian.dev/drill: observability",
		"guardian-observability-drill job=$JOB_NAME phase=start",
		"guardian-observability-drill job=$JOB_NAME phase=complete",
		"guardian-observability-drill job=$JOB_NAME phase=cleanup-warning",
		"name: postgres-guardian-superuser",
		"value: postgres-guardian-rw",
		"value: guardian_root_observability_20260624t030405z",
		"readOnlyRootFilesystem: true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("manifest missing %q:\n%s", want, got)
		}
	}
}

func TestParseJobTerminalStatus(t *testing.T) {
	complete, err := parseJobTerminalStatus(`{"status":{"conditions":[{"type":"Complete","status":"True","reason":"Completed","message":"done"}]}}`)
	if err != nil {
		t.Fatalf("parse complete job: %v", err)
	}
	if !complete.complete || complete.failed {
		t.Fatalf("complete status = %#v", complete)
	}

	failed, err := parseJobTerminalStatus(`{"status":{"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"Job has reached the specified backoff limit"}]}}`)
	if err != nil {
		t.Fatalf("parse failed job: %v", err)
	}
	if !failed.failed || failed.complete {
		t.Fatalf("failed status = %#v", failed)
	}
	if !strings.Contains(failed.message, "BackoffLimitExceeded") {
		t.Fatalf("failed message = %q, want reason", failed.message)
	}

	running, err := parseJobTerminalStatus(`{"status":{"conditions":[{"type":"Complete","status":"False"}]}}`)
	if err != nil {
		t.Fatalf("parse running job: %v", err)
	}
	if running.complete || running.failed {
		t.Fatalf("running status = %#v, want non-terminal", running)
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

func TestVictoriaMetricsQueriesIncludeHubble(t *testing.T) {
	cfg := observabilityConfig{
		Namespace:       "tenant-root",
		ApplicationName: "guardian",
		Name:            "guardian-root-observability-20260624t030405z",
	}
	queries := victoriaMetricsQueries(cfg)
	joined := make([]string, 0, len(queries))
	for _, query := range queries {
		joined = append(joined, query.label+"="+query.query)
	}
	got := strings.Join(joined, "\n")
	for _, want := range []string{
		`Postgres scrape targets in VictoriaMetrics=sum(up{namespace="tenant-root",job="tenant-root/postgres-guardian"})`,
		"Hubble flow metrics in VictoriaMetrics=sum(hubble_flows_processed_total)",
		"Hubble TCP metrics in VictoriaMetrics=sum(hubble_tcp_flags_total)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("victoriaMetricsQueries missing %q:\n%s", want, got)
		}
	}
}
