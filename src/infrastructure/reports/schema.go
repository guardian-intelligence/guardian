package reports

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = "guardian.infrastructure.report.v1"

var (
	validReportTypes = map[string]bool{
		"load_test":          true,
		"disaster_recovery":  true,
		"single_node_outage": true,
	}
	validComponents = map[string]bool{
		"cnpg_postgres":       true,
		"harbor":              true,
		"clickhouse":          true,
		"openbao":             true,
		"cozystack_dashboard": true,
		"company_site":        true,
	}
	validEnvironments = map[string]bool{
		"root":  true,
		"dev":   true,
		"gamma": true,
		"prod":  true,
	}
	validResults = map[string]bool{
		"pass": true,
		"fail": true,
	}

	placeholderPattern = regexp.MustCompile(`(?i)\b(todo|tbd|placeholder|lorem|example\.com|REPLACE_WITH)\b`)
	secretPattern      = regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16}|BEGIN [A-Z ]*PRIVATE KEY|aws_secret_access_key\s*[:=]|cloudflare.*token\s*[:=]|bao_token\s*[:=]|vault_token\s*[:=])`)
	shaPattern         = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

type Report struct {
	SchemaVersion  string        `json:"schema_version"`
	ReportType     string        `json:"report_type"`
	Component      string        `json:"component"`
	Environment    string        `json:"environment"`
	Cluster        string        `json:"cluster"`
	SourceRevision string        `json:"source_revision"`
	StartedAt      string        `json:"started_at"`
	FinishedAt     string        `json:"finished_at"`
	Target         Target        `json:"target"`
	Procedure      []string      `json:"procedure"`
	Checks         []Check       `json:"checks"`
	Measurements   []Measurement `json:"measurements,omitempty"`
	Artifacts      []Artifact    `json:"artifacts,omitempty"`
	Conclusion     string        `json:"conclusion"`
	Notes          string        `json:"notes,omitempty"`
}

type Target struct {
	Namespace string `json:"namespace"`
	APIGroup  string `json:"api_group"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Endpoint  string `json:"endpoint,omitempty"`
}

type Check struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	Result     string `json:"result"`
	ObservedAt string `json:"observed_at"`
	Summary    string `json:"summary"`
}

type Measurement struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	URI    string `json:"uri,omitempty"`
}

type CoverageKey struct {
	ReportType  string
	Component   string
	Environment string
}

type TargetKey struct {
	Component   string
	Environment string
}

func ExpectedCoverage() []CoverageKey {
	var out []CoverageKey
	for _, component := range []string{"cnpg_postgres", "harbor", "clickhouse"} {
		out = appendCoverage(out, component, []string{"root", "dev", "gamma", "prod"})
	}
	out = appendCoverage(out, "openbao", []string{"root"})
	out = appendCoverage(out, "cozystack_dashboard", []string{"root"})
	out = appendCoverage(out, "company_site", []string{"dev", "gamma", "prod"})
	return out
}

func ExpectedTarget(component, environment string) (Target, bool) {
	target, ok := expectedTargets()[TargetKey{
		Component:   component,
		Environment: environment,
	}]
	return target, ok
}

func Coverage(report Report) CoverageKey {
	return CoverageKey{
		ReportType:  report.ReportType,
		Component:   report.Component,
		Environment: report.Environment,
	}
}

func MissingCoverage(reports []Report) []CoverageKey {
	seen := map[CoverageKey]bool{}
	for _, report := range reports {
		seen[Coverage(report)] = true
	}

	var missing []CoverageKey
	for _, expected := range ExpectedCoverage() {
		if !seen[expected] {
			missing = append(missing, expected)
		}
	}
	return missing
}

func UnexpectedCoverage(reports []Report) []CoverageKey {
	expected := expectedCoverageSet()
	seenUnexpected := map[CoverageKey]bool{}
	var unexpected []CoverageKey
	for _, report := range reports {
		key := Coverage(report)
		if expected[key] || seenUnexpected[key] {
			continue
		}
		seenUnexpected[key] = true
		unexpected = append(unexpected, key)
	}
	return unexpected
}

func Decode(data []byte) (Report, error) {
	var report Report
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		return Report{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Report{}, errors.New("multiple JSON values")
	}
	return report, nil
}

func Validate(report Report) error {
	var errs []string
	require(&errs, report.SchemaVersion == SchemaVersion, "schema_version must be "+SchemaVersion)
	require(&errs, validReportTypes[report.ReportType], "report_type must be load_test, disaster_recovery, or single_node_outage")
	require(&errs, validComponents[report.Component], "component is not one of the required infrastructure components")
	require(&errs, validEnvironments[report.Environment], "environment must be root, dev, gamma, or prod")
	require(&errs, report.Cluster == "guardian-mgmt", "cluster must be guardian-mgmt")
	require(&errs, shaPattern.MatchString(report.SourceRevision), "source_revision must be a 40 or 64 character lowercase hex commit/digest")
	require(&errs, validResults[report.Conclusion], "conclusion must be pass or fail")

	startedAt, startedOK := parseTime(&errs, "started_at", report.StartedAt)
	finishedAt, finishedOK := parseTime(&errs, "finished_at", report.FinishedAt)
	if startedOK && finishedOK {
		require(&errs, !finishedAt.Before(startedAt), "finished_at must be at or after started_at")
	}

	validateTarget(&errs, report.Target)
	validateExpectedTarget(&errs, report)
	require(&errs, len(report.Procedure) > 0, "procedure must contain at least one step")
	for i, step := range report.Procedure {
		requireText(&errs, fmt.Sprintf("procedure[%d]", i), step)
	}

	require(&errs, len(report.Checks) > 0, "checks must contain at least one check")
	allChecksPass := true
	for i, check := range report.Checks {
		validateCheck(&errs, i, check)
		if check.Result != "pass" {
			allChecksPass = false
		}
	}
	if report.Conclusion == "pass" {
		require(&errs, allChecksPass, "passing reports cannot contain failing checks")
	}

	for i, measurement := range report.Measurements {
		validateMeasurement(&errs, i, measurement)
	}
	for i, artifact := range report.Artifacts {
		validateArtifact(&errs, i, artifact)
	}
	requireNoBannedText(&errs, "notes", report.Notes)
	validateReportTypeEvidence(&errs, report)

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validateTarget(errs *[]string, target Target) {
	requireText(errs, "target.namespace", target.Namespace)
	requireText(errs, "target.api_group", target.APIGroup)
	requireText(errs, "target.kind", target.Kind)
	requireText(errs, "target.name", target.Name)
	requireNoBannedText(errs, "target.endpoint", target.Endpoint)
}

func validateExpectedTarget(errs *[]string, report Report) {
	expected, ok := ExpectedTarget(report.Component, report.Environment)
	if !ok {
		return
	}
	requireTargetField(errs, "target.namespace", report.Target.Namespace, expected.Namespace, report)
	requireTargetField(errs, "target.api_group", report.Target.APIGroup, expected.APIGroup, report)
	requireTargetField(errs, "target.kind", report.Target.Kind, expected.Kind, report)
	requireTargetField(errs, "target.name", report.Target.Name, expected.Name, report)
	requireTargetField(errs, "target.endpoint", report.Target.Endpoint, expected.Endpoint, report)
}

func requireTargetField(errs *[]string, field, got, want string, report Report) {
	require(errs, got == want, fmt.Sprintf("%s must be %q for %s/%s reports", field, want, report.Component, report.Environment))
}

func validateCheck(errs *[]string, i int, check Check) {
	prefix := fmt.Sprintf("checks[%d]", i)
	requireText(errs, prefix+".name", check.Name)
	requireText(errs, prefix+".command", check.Command)
	require(errs, validResults[check.Result], prefix+".result must be pass or fail")
	parseTime(errs, prefix+".observed_at", check.ObservedAt)
	requireText(errs, prefix+".summary", check.Summary)
}

func validateMeasurement(errs *[]string, i int, measurement Measurement) {
	prefix := fmt.Sprintf("measurements[%d]", i)
	requireText(errs, prefix+".name", measurement.Name)
	requireText(errs, prefix+".unit", measurement.Unit)
	require(errs, measurement.Value >= 0, prefix+".value must be non-negative")
}

func validateArtifact(errs *[]string, i int, artifact Artifact) {
	prefix := fmt.Sprintf("artifacts[%d]", i)
	requireText(errs, prefix+".name", artifact.Name)
	require(errs, regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(artifact.SHA256), prefix+".sha256 must be lowercase hex sha256")
	requireNoBannedText(errs, prefix+".uri", artifact.URI)
}

func validateReportTypeEvidence(errs *[]string, report Report) {
	switch report.ReportType {
	case "load_test":
		require(errs, len(report.Measurements) > 0, "load_test reports must include measurements")
	case "disaster_recovery":
		require(errs, hasCheckMatching(report.Checks, "restore"), "disaster_recovery reports must include a restore check")
		require(errs, hasMeasurement(report.Measurements, "recovery_seconds"), "disaster_recovery reports must include recovery_seconds")
	case "single_node_outage":
		require(errs, hasCheckMatching(report.Checks, "node"), "single_node_outage reports must include a node check")
		require(errs, hasMeasurement(report.Measurements, "recovery_seconds"), "single_node_outage reports must include recovery_seconds")
	}
}

func hasCheckMatching(checks []Check, needle string) bool {
	for _, check := range checks {
		if strings.Contains(strings.ToLower(check.Name), needle) || strings.Contains(strings.ToLower(check.Summary), needle) {
			return true
		}
	}
	return false
}

func hasMeasurement(measurements []Measurement, name string) bool {
	for _, measurement := range measurements {
		if measurement.Name == name {
			return true
		}
	}
	return false
}

func parseTime(errs *[]string, field, value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		*errs = append(*errs, field+" must be RFC3339")
		return time.Time{}, false
	}
	return parsed, true
}

func requireText(errs *[]string, field, value string) {
	require(errs, strings.TrimSpace(value) != "", field+" must not be empty")
	requireNoBannedText(errs, field, value)
}

func requireNoBannedText(errs *[]string, field, value string) {
	if value == "" {
		return
	}
	require(errs, !placeholderPattern.MatchString(value), field+" contains placeholder text")
	require(errs, !secretPattern.MatchString(value), field+" contains secret-looking text")
}

func require(errs *[]string, ok bool, msg string) {
	if !ok {
		*errs = append(*errs, msg)
	}
}

func appendCoverage(out []CoverageKey, component string, environments []string) []CoverageKey {
	for _, environment := range environments {
		for _, reportType := range []string{"load_test", "disaster_recovery", "single_node_outage"} {
			out = append(out, CoverageKey{
				ReportType:  reportType,
				Component:   component,
				Environment: environment,
			})
		}
	}
	return out
}

func expectedTargets() map[TargetKey]Target {
	out := map[TargetKey]Target{}
	for _, environment := range []string{"root", "dev", "gamma", "prod"} {
		namespace := tenantNamespace(environment)
		out[TargetKey{Component: "cnpg_postgres", Environment: environment}] = Target{
			Namespace: namespace,
			APIGroup:  "apps.cozystack.io",
			Kind:      "Postgres",
			Name:      "guardian",
		}
		out[TargetKey{Component: "harbor", Environment: environment}] = Target{
			Namespace: namespace,
			APIGroup:  "apps.cozystack.io",
			Kind:      "Harbor",
			Name:      "guardian",
			Endpoint:  harborEndpoint(environment),
		}
		out[TargetKey{Component: "clickhouse", Environment: environment}] = Target{
			Namespace: namespace,
			APIGroup:  "apps.cozystack.io",
			Kind:      "ClickHouse",
			Name:      "guardian",
		}
	}

	out[TargetKey{Component: "openbao", Environment: "root"}] = Target{
		Namespace: "tenant-root",
		APIGroup:  "apps.cozystack.io",
		Kind:      "OpenBAO",
		Name:      "guardian",
	}
	out[TargetKey{Component: "cozystack_dashboard", Environment: "root"}] = Target{
		Namespace: "cozy-dashboard",
		APIGroup:  "networking.k8s.io",
		Kind:      "Ingress",
		Name:      "dashboard-web-ingress",
		Endpoint:  "https://dashboard.guardianintelligence.org",
	}
	for _, environment := range []string{"dev", "gamma", "prod"} {
		out[TargetKey{Component: "company_site", Environment: environment}] = Target{
			Namespace: tenantNamespace(environment),
			APIGroup:  "apps",
			Kind:      "Deployment",
			Name:      "company-site",
			Endpoint:  companySiteEndpoint(environment),
		}
	}
	return out
}

func tenantNamespace(environment string) string {
	if environment == "root" {
		return "tenant-root"
	}
	return "tenant-" + environment
}

func harborEndpoint(environment string) string {
	switch environment {
	case "root":
		return "https://harbor.guardianintelligence.org"
	default:
		return "https://harbor." + environment + ".gi.org"
	}
}

func companySiteEndpoint(environment string) string {
	switch environment {
	case "prod":
		return "https://guardianintelligence.org"
	default:
		return "https://" + environment + ".gi.org"
	}
}

func expectedCoverageSet() map[CoverageKey]bool {
	out := map[CoverageKey]bool{}
	for _, expected := range ExpectedCoverage() {
		out[expected] = true
	}
	return out
}
