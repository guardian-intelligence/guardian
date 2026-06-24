package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDNSTargetsFromDNSEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dns.yaml")
	writeFile(t, path, `
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
spec:
  endpoints:
    - dnsName: "*.guardianintelligence.org"
      recordType: A
      targets:
        - 206.223.228.101
    - dnsName: harbor.guardianintelligence.org.
      recordType: A
      targets:
        - 206.223.228.101
`)

	targets, err := loadDNSTargets([]string{path}, "edge-health-wildcard")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	wildcard := targets[0]
	if wildcard.DNSName != "*.guardianintelligence.org" {
		t.Fatalf("wildcard DNSName = %q", wildcard.DNSName)
	}
	if wildcard.QueryName != "edge-health-wildcard.guardianintelligence.org" {
		t.Fatalf("wildcard QueryName = %q", wildcard.QueryName)
	}
	if got, want := wildcard.ExpectedValues, []string{"206.223.228.101"}; !sameStringSet(got, want) {
		t.Fatalf("wildcard ExpectedValues = %v, want %v", got, want)
	}
}

func TestLoadHTTPTargetsFromPrometheusFileSD(t *testing.T) {
	dnsTargets := []dnsTarget{
		{
			DNSName:        "*.guardianintelligence.org",
			QueryName:      "edge-health-wildcard.guardianintelligence.org",
			RecordType:     "A",
			ExpectedValues: []string{"206.223.228.101"},
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
	if got, want := target.ExpectedIPs, []string{"206.223.228.101"}; !sameStringSet(got, want) {
		t.Fatalf("ExpectedIPs = %v, want %v", got, want)
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

func TestRunDNSFailsWhenAnswersDoNotMatch(t *testing.T) {
	doggo := writeExecutable(t, "doggo", `#!/bin/sh
cat <<'JSON'
{"responses":[{"answers":[{"type":"A","address":"203.0.113.10"}]}]}
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
			DNSName:        "harbor.guardianintelligence.org",
			QueryName:      "harbor.guardianintelligence.org",
			RecordType:     "A",
			ExpectedValues: []string{"206.223.228.101"},
		},
	}, []string{"1.1.1.1"})
	if err == nil {
		t.Fatal("runDNS succeeded, want mismatch failure")
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
		OriginChecks:           false,
	}
	err := runHTTP(context.Background(), cfg, []httpTarget{
		{
			URL:              "https://harbor.guardianintelligence.org/v2/",
			Host:             "harbor.guardianintelligence.org",
			Surface:          "harbor",
			Stage:            "root",
			Name:             "guardian-edge-root-harbor",
			ExpectedStatuses: []int{200, 401},
			ExpectedIPs:      []string{"206.223.228.101"},
		},
	})
	if err == nil {
		t.Fatal("runHTTP succeeded, want k6 failure")
	}
	if !strings.Contains(err.Error(), "k6 public-dns") {
		t.Fatalf("runHTTP error = %v", err)
	}
}

func TestSameStringSetNormalizesOrdering(t *testing.T) {
	if !sameStringSet([]string{"b", "a", "a"}, []string{"a", "b"}) {
		t.Fatal("sameStringSet returned false for equivalent sets")
	}
	if sameStringSet([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("sameStringSet returned true for different sets")
	}
}

func TestWildcardMatchesOnlySubdomains(t *testing.T) {
	if !wildcardMatches("*.guardianintelligence.org", "harbor.guardianintelligence.org") {
		t.Fatal("wildcard did not match subdomain")
	}
	if wildcardMatches("*.guardianintelligence.org", "guardianintelligence.org") {
		t.Fatal("wildcard matched apex")
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
