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
