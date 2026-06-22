package reports

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAcceptsCompleteLoadReport(t *testing.T) {
	report := validReport("load_test")
	if err := Validate(report); err != nil {
		t.Fatalf("Validate(valid load report): %v", err)
	}
}

func TestValidateRejectsPlaceholdersAndSecretLookingText(t *testing.T) {
	report := validReport("load_test")
	report.Procedure[0] = "TODO run this later"
	report.Checks[0].Summary = "aws_secret_access_key=not-for-git"

	err := Validate(report)
	if err == nil {
		t.Fatal("Validate accepted placeholder and secret-looking text")
	}
	msg := err.Error()
	for _, want := range []string{"procedure[0] contains placeholder text", "checks[0].summary contains secret-looking text"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Validate error %q does not contain %q", msg, want)
		}
	}
}

func TestValidateEnforcesDisasterRecoveryEvidence(t *testing.T) {
	report := validReport("disaster_recovery")
	report.Checks = []Check{{
		Name:       "backup listed",
		Command:    "kubectl -n tenant-dev get backups.backups.cozystack.io",
		Result:     "pass",
		ObservedAt: "2026-06-22T07:00:10Z",
		Summary:    "backup artifact was present",
	}}
	report.Measurements = nil

	err := Validate(report)
	if err == nil {
		t.Fatal("Validate accepted DR report without restore evidence")
	}
	msg := err.Error()
	for _, want := range []string{"disaster_recovery reports must include a restore check", "disaster_recovery reports must include recovery_seconds"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Validate error %q does not contain %q", msg, want)
		}
	}
}

func TestValidateEnforcesSingleNodeOutageEvidence(t *testing.T) {
	report := validReport("single_node_outage")
	report.Checks = []Check{{
		Name:       "service remained ready",
		Command:    "kubectl -n tenant-dev get deploy/company-site",
		Result:     "pass",
		ObservedAt: "2026-06-22T07:00:10Z",
		Summary:    "deployment stayed available",
	}}
	report.Measurements = []Measurement{{Name: "available_replicas", Unit: "count", Value: 3}}

	err := Validate(report)
	if err == nil {
		t.Fatal("Validate accepted outage report without node recovery evidence")
	}
	msg := err.Error()
	for _, want := range []string{"single_node_outage reports must include a node check", "single_node_outage reports must include recovery_seconds"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Validate error %q does not contain %q", msg, want)
		}
	}
}

func TestDecodeRejectsUnknownFieldsAndMultipleJSONValues(t *testing.T) {
	data, err := json.Marshal(validReport("load_test"))
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(data), `"notes":`, `"unknown":true,"notes":`, 1)
	if _, err := Decode([]byte(withUnknown)); err == nil {
		t.Fatal("Decode accepted unknown field")
	}
	if _, err := Decode(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("Decode accepted multiple JSON values")
	}
}

func TestCheckedInReports(t *testing.T) {
	root := runfilePath("src/infrastructure/reports/checked-in")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return
	}

	var checked int
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		checked++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		report, err := Decode(data)
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if err := Validate(report); err != nil {
			t.Fatalf("validate %s: %v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk checked-in reports: %v", err)
	}
	t.Logf("validated %d checked-in reports", checked)
}

func validReport(reportType string) Report {
	report := Report{
		SchemaVersion:  SchemaVersion,
		ReportType:     reportType,
		Component:      "company_site",
		Environment:    "dev",
		Cluster:        "guardian-mgmt",
		SourceRevision: "0123456789abcdef0123456789abcdef01234567",
		StartedAt:      "2026-06-22T07:00:00Z",
		FinishedAt:     "2026-06-22T07:05:00Z",
		Target: Target{
			Namespace: "tenant-dev",
			APIGroup:  "apps",
			Kind:      "Deployment",
			Name:      "company-site",
			Endpoint:  "https://dev.gi.org",
		},
		Procedure: []string{
			"Applied the merged source revision through Flux.",
			"Collected live Kubernetes and endpoint observations.",
		},
		Checks: []Check{{
			Name:       "endpoint health",
			Command:    "curl --fail https://dev.gi.org/healthz",
			Result:     "pass",
			ObservedAt: "2026-06-22T07:01:00Z",
			Summary:    "health endpoint returned ok",
		}},
		Measurements: []Measurement{{
			Name:  "requests_per_second",
			Unit:  "rps",
			Value: 25,
		}},
		Artifacts: []Artifact{{
			Name:   "raw-output",
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			URI:    "s3://guardian-vault/reports/company-site-dev.txt",
		}},
		Conclusion: "pass",
		Notes:      "Report produced from live observations.",
	}

	switch reportType {
	case "disaster_recovery":
		report.Checks[0].Name = "restore completed"
		report.Checks[0].Command = "kubectl -n tenant-dev get restorejobs.backups.cozystack.io"
		report.Checks[0].Summary = "restore job completed and target health check passed"
		report.Measurements = []Measurement{{Name: "recovery_seconds", Unit: "seconds", Value: 120}}
	case "single_node_outage":
		report.Checks[0].Name = "node outage recovery"
		report.Checks[0].Command = "kubectl get nodes"
		report.Checks[0].Summary = "node was drained or lost and workload availability recovered"
		report.Measurements = []Measurement{{Name: "recovery_seconds", Unit: "seconds", Value: 45}}
	}
	return report
}

func runfilePath(rel string) string {
	if testSrcdir, workspace := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); testSrcdir != "" && workspace != "" {
		return filepath.Join(testSrcdir, workspace, rel)
	}
	return rel
}
