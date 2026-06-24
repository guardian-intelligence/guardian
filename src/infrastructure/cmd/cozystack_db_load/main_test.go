package main

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultJobName(t *testing.T) {
	got := defaultJobName("root", "postgres", time.Date(2026, 6, 22, 13, 14, 15, 0, time.UTC))
	want := "guardian-root-postgres-load-20260622t131415z"
	if got != want {
		t.Fatalf("defaultJobName() = %q, want %q", got, want)
	}
}

func TestPostgresJobManifest(t *testing.T) {
	cfg := baseConfig("postgres")
	got := postgresJobManifest(cfg)
	for _, want := range []string{
		"kind: Job\nmetadata:\n  name: guardian-root-postgres-load-test\n",
		"namespace: tenant-root\n",
		"guardian.dev/component: postgres\n",
		"image: " + postgresBenchImage + "\n",
		"value: postgres-guardian-rw\n",
		"name: postgres-guardian-superuser\n                  key: username\n",
		"name: postgres-guardian-superuser\n                  key: password\n",
		"name: HOME\n              value: /tmp\n",
		"pgbench --initialize --scale \"$PGBENCH_SCALE\"",
		"pgbench --client \"$PGBENCH_CLIENTS\"",
		"value: guardian_root_postgres_load_test\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("postgres manifest missing %q:\n%s", want, got)
		}
	}
}

func TestClickHouseJobManifest(t *testing.T) {
	cfg := baseConfig("clickhouse")
	got := clickHouseJobManifest(cfg)
	for _, want := range []string{
		"kind: Job\nmetadata:\n  name: guardian-root-clickhouse-load-test\n",
		"namespace: tenant-root\n",
		"guardian.dev/component: clickhouse\n",
		"image: " + clickhouseBenchImage + "\n",
		"value: chendpoint-clickhouse-guardian\n",
		"name: clickhouse-guardian-credentials\n                  key: backup\n",
		"name: HOME\n              value: /tmp\n",
		"clickhouse-benchmark --host \"$CLICKHOUSE_HOST\"",
		"--query \"$CLICKHOUSE_QUERY\"",
		"SELECT sum(number) FROM numbers(1000000)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("clickhouse manifest missing %q:\n%s", want, got)
		}
	}
}

func TestValidateConfig(t *testing.T) {
	cfg := baseConfig("postgres")
	cfg.Kubectl = "/kubectl"
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	badName := cfg
	badName.Name = "not_a_dns_label"
	if err := validateConfig(badName); err == nil {
		t.Fatalf("invalid job name was accepted")
	}

	badScale := cfg
	badScale.PgbenchScale = "0"
	if err := validateConfig(badScale); err == nil {
		t.Fatalf("zero pgbench scale was accepted")
	}

	badQuery := cfg
	badQuery.ClickHouseQuery = ""
	if err := validateConfig(badQuery); err == nil {
		t.Fatalf("empty clickhouse query was accepted")
	}
}

func TestNamespaceAndComponentValidation(t *testing.T) {
	if got, err := namespaceForStage("root"); err != nil || got != "tenant-root" {
		t.Fatalf("namespaceForStage(root) = %q, %v", got, err)
	}
	if _, err := namespaceForStage("prod"); err == nil {
		t.Fatalf("retired product stage was accepted")
	}
	if _, err := namespaceForStage("staging"); err == nil {
		t.Fatalf("invalid stage was accepted")
	}

	if got, err := componentName("cnpg"); err != nil || got != "postgres" {
		t.Fatalf("componentName(cnpg) = %q, %v", got, err)
	}
	if got, err := componentName("ch"); err != nil || got != "clickhouse" {
		t.Fatalf("componentName(ch) = %q, %v", got, err)
	}
	if got := componentResource("postgres"); got != "postgreses.apps.cozystack.io" {
		t.Fatalf("componentResource(postgres) = %q", got)
	}
	if got := componentResource("clickhouse"); got != "clickhouses.apps.cozystack.io" {
		t.Fatalf("componentResource(clickhouse) = %q", got)
	}
	if _, err := componentName("harbor"); err == nil {
		t.Fatalf("invalid component was accepted")
	}
}

func baseConfig(component string) dbLoadConfig {
	name := "guardian-root-" + component + "-load-test"
	return dbLoadConfig{
		Kubectl:                   "/kubectl",
		RequestTimeout:            "15s",
		WaitTimeout:               "20m",
		Stage:                     "root",
		Namespace:                 "tenant-root",
		Component:                 component,
		ApplicationName:           "guardian",
		Name:                      name,
		TTLSecondsAfterFinished:   "86400",
		PgbenchScale:              "10",
		PgbenchClients:            "4",
		PgbenchJobs:               "2",
		PgbenchDurationSeconds:    "60",
		ClickHouseConcurrency:     "4",
		ClickHouseIterations:      "100",
		ClickHouseDurationSeconds: "60",
		ClickHouseQuery:           "SELECT sum(number) FROM numbers(1000000)",
	}
}
