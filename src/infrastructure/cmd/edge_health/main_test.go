package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDNSTargetsFromPrometheusFileSD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dns.yaml")
	writeFile(t, path, `
- targets:
    - edge-health-wildcard.guardianintelligence.org
    - harbor.guardianintelligence.org.
  labels:
    guardian_record_type: A
`)

	targets, err := loadDNSTargets([]string{path}, "guardianintelligence.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	wildcard := targets[0]
	if wildcard.DNSName != "edge-health-wildcard.guardianintelligence.org" {
		t.Fatalf("wildcard DNSName = %q", wildcard.DNSName)
	}
	if wildcard.QueryName != "edge-health-wildcard.guardianintelligence.org" {
		t.Fatalf("wildcard QueryName = %q", wildcard.QueryName)
	}
}

func TestLoadHTTPTargetsFromPrometheusFileSD(t *testing.T) {
	dnsTargets := []dnsTarget{
		{
			DNSName:    "harbor.guardianintelligence.org",
			QueryName:  "harbor.guardianintelligence.org",
			RecordType: "A",
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "targets.yaml")
	writeFile(t, path, `
- targets:
    - https://harbor.guardianintelligence.org/v2/
  labels:
    guardian_surface: harbor
    guardian_stage: root
    guardian_expected_statuses: "200,401"
`)

	targets, err := loadHTTPTargets([]string{path}, dnsTargets, "guardianintelligence.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	target := targets[0]
	if target.Host != "harbor.guardianintelligence.org" {
		t.Fatalf("Host = %q", target.Host)
	}
	if got, want := target.ExpectedStatuses, []int{200, 401}; !sameInts(got, want) {
		t.Fatalf("ExpectedStatuses = %v, want %v", got, want)
	}
}

func TestLoadHTTPTargetsRejectsHostOutsideDNS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "targets.yaml")
	writeFile(t, path, `
- targets:
    - https://harbor.guardianintelligence.org/v2/
  labels:
    guardian_surface: harbor
`)

	_, err := loadHTTPTargets([]string{path}, nil, "guardianintelligence.org")
	if err == nil {
		t.Fatal("loadHTTPTargets succeeded, want error")
	}
}

func TestParseExpectedStatusesAcceptsStatusClass(t *testing.T) {
	statuses, err := parseExpectedStatuses("2xx,401")
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 101 {
		t.Fatalf("statuses = %d, want 101", len(statuses))
	}
	for _, status := range []int{200, 204, 299, 401} {
		if !containsInt(statuses, status) {
			t.Fatalf("statuses missing %d: %v", status, statuses)
		}
	}
	if containsInt(statuses, 199) || containsInt(statuses, 300) {
		t.Fatalf("statuses should only include 2xx plus explicit statuses: %v", statuses)
	}
}

func TestRunDNSFailsWhenQueryDoesNotAnswer(t *testing.T) {
	doggo := writeExecutable(t, "doggo", `#!/bin/sh
cat <<'JSON'
{"responses":[{"answers":[]}]}
JSON
`)
	cfg := config{
		Doggo:              doggo,
		DNSTimeout:         "1s",
		DNSSamples:         2,
		DNSMinSuccessRatio: 1,
	}
	err := runDNS(context.Background(), cfg, []dnsTarget{
		{
			DNSName:    "harbor.guardianintelligence.org",
			QueryName:  "harbor.guardianintelligence.org",
			RecordType: "A",
		},
	}, []string{"1.1.1.1"})
	if err == nil {
		t.Fatal("runDNS succeeded, want unanswered failure")
	}
	if !strings.Contains(err.Error(), "DNS confidence below threshold") {
		t.Fatalf("runDNS error = %v", err)
	}
}

func TestRunHTTPPropagatesK6Failure(t *testing.T) {
	k6 := writeExecutable(t, "k6", "#!/bin/sh\nexit 99\n")
	cfg := config{
		K6:                     k6,
		Script:                 "/does/not/matter.js",
		HTTPVUs:                "1",
		HTTPIterations:         1,
		HTTPMaxDuration:        "1s",
		HTTPRequestTimeout:     "1s",
		HTTPSleepSeconds:       "0",
		K6ExpectedStatusCutoff: "rate>0.99",
	}
	err := runHTTP(context.Background(), cfg, []httpTarget{
		{
			URL:              "https://harbor.guardianintelligence.org/v2/",
			Host:             "harbor.guardianintelligence.org",
			Surface:          "harbor",
			Stage:            "root",
			Name:             "guardian-edge-root-harbor",
			ExpectedStatuses: []int{200, 401},
		},
	})
	if err == nil {
		t.Fatal("runHTTP succeeded, want k6 failure")
	}
	if !strings.Contains(err.Error(), "k6 public-edge") {
		t.Fatalf("runHTTP error = %v", err)
	}
}

func sameInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
