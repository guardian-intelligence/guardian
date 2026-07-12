package tests

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLogPipelineCanaryContract(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/alerting/log-pipeline-health.yaml")
	docs := yamlDocs(t, path)

	scrape := findDoc(t, docs, "VMServiceScrape", "fluent-bit")
	assertNestedString(t, scrape, "tenant-root", "metadata", "namespace")
	matchNames := sliceValue(nestedValue(t, scrape, "spec", "namespaceSelector", "matchNames"))
	if len(matchNames) != 1 || stringValue(matchNames[0]) != "cozy-monitoring" {
		t.Fatalf("Fluent Bit scrape namespaces = %#v, want only cozy-monitoring", matchNames)
	}
	assertNestedString(t, scrape, "fluent-bit", "spec", "selector", "matchLabels", "app.kubernetes.io/name")
	endpoints := sliceValue(nestedValue(t, scrape, "spec", "endpoints"))
	if len(endpoints) != 1 ||
		stringValue(mapValue(endpoints[0])["port"]) != "http" ||
		stringValue(mapValue(endpoints[0])["path"]) != "/api/v2/metrics/prometheus" {
		t.Fatalf("Fluent Bit scrape endpoints = %#v, want named http port at /api/v2/metrics/prometheus", endpoints)
	}

	daemonSet := findDoc(t, docs, "DaemonSet", "guardian-log-sanitizer-canary")
	podSpec := nestedMap(t, daemonSet, "spec", "template", "spec")
	assertNestedBool(t, podSpec, false, "automountServiceAccountToken")
	assertNestedBool(t, podSpec, false, "enableServiceLinks")
	assertNestedBool(t, podSpec, true, "securityContext", "runAsNonRoot")
	container := mapValue(sliceValue(nestedValue(t, podSpec, "containers"))[0])
	assertNestedString(t, container, "canary", "name")
	assertNestedBool(t, container, false, "securityContext", "allowPrivilegeEscalation")
	assertNestedBool(t, container, true, "securityContext", "readOnlyRootFilesystem")
	envs := sliceValue(container["env"])
	if len(envs) != 1 {
		t.Fatalf("canary env = %#v, want only GUARDIAN_NODE", envs)
	}
	nodeEnv := mapValue(envs[0])
	if stringValue(nodeEnv["name"]) != "GUARDIAN_NODE" ||
		stringValue(nestedValue(t, nodeEnv, "valueFrom", "fieldRef", "fieldPath")) != "spec.nodeName" {
		t.Fatalf("canary node env = %#v, want GUARDIAN_NODE from spec.nodeName", nodeEnv)
	}
	args := sliceValue(container["args"])
	if len(args) != 1 {
		t.Fatalf("canary has %d args, want one loop script", len(args))
	}
	script, ok := args[0].(string)
	if !ok {
		t.Fatal("canary loop script is not a string")
	}
	const diagnostic = `"guardian_log_pipeline_canary":"diagnostic_ok"`
	const sanitizer = `"guardian_log_sanitizer_canary":"api-key=`
	assertTextContains(t, script, diagnostic, path)
	assertTextContains(t, script, sanitizer, path)
	diagnosticLine, sanitizerLine := -1, -1
	for i, line := range strings.Split(script, "\n") {
		if strings.Contains(line, diagnostic) {
			diagnosticLine = i
		}
		if strings.Contains(line, sanitizer) {
			sanitizerLine = i
		}
	}
	if diagnosticLine < 0 || sanitizerLine < 0 || diagnosticLine == sanitizerLine {
		t.Fatal("diagnostic and sanitizer fixtures must be separate log lines")
	}

	match := regexp.MustCompile(`api-key=(guardian-fake-credential-[a-z0-9-]+)`).FindStringSubmatch(script)
	if len(match) != 2 {
		t.Fatal("canary must emit one obviously inert guardian-fake-credential fixture")
	}
	fixture := match[1]
	assertTextContains(t, script, `"guardian_node":"%s"`, path)
	assertTextContains(t, script, `"$GUARDIAN_NODE"`, path)

	networkPolicy := findDoc(t, docs, "NetworkPolicy", "guardian-log-sanitizer-canary")
	policySpec := nestedMap(t, networkPolicy, "spec")
	policyTypes := sliceValue(policySpec["policyTypes"])
	if len(policyTypes) != 2 || stringValue(policyTypes[0]) != "Ingress" || stringValue(policyTypes[1]) != "Egress" {
		t.Fatalf("canary NetworkPolicy policyTypes = %#v, want Ingress and Egress", policyTypes)
	}
	if _, ok := policySpec["ingress"]; ok {
		t.Fatal("canary deny-all NetworkPolicy unexpectedly declares ingress rules")
	}
	if _, ok := policySpec["egress"]; ok {
		t.Fatal("canary deny-all NetworkPolicy unexpectedly declares egress rules")
	}

	configMap := findDoc(t, docs, "ConfigMap", "guardian-log-pipeline-vlogs-rules")
	rulesText, ok := nestedValue(t, configMap, "data", "log-pipeline.yaml").(string)
	if !ok {
		t.Fatal("embedded log-pipeline rules are not a string")
	}
	var rulesDoc map[string]interface{}
	if err := yaml.Unmarshal([]byte(rulesText), &rulesDoc); err != nil {
		t.Fatalf("parse embedded log-pipeline rules: %v", err)
	}
	groups := sliceValue(rulesDoc["groups"])
	if len(groups) != 1 {
		t.Fatalf("embedded rules have %d groups, want one bounded canary group", len(groups))
	}
	rules := sliceValue(mapValue(groups[0])["rules"])
	if len(rules) != 3 {
		t.Fatalf("embedded rules have %d alerts, want diagnostic, marker, and leak checks", len(rules))
	}

	byAlert := make(map[string]map[string]interface{}, len(rules))
	for _, raw := range rules {
		rule := mapValue(raw)
		alert, ok := rule["alert"].(string)
		if !ok || byAlert[alert] != nil {
			t.Fatalf("invalid or duplicate canary alert %q", alert)
		}
		byAlert[alert] = rule
		expr, ok := rule["expr"].(string)
		if !ok || !strings.Contains(expr, "| stats ") {
			t.Fatalf("canary alert %q does not reduce to a stable stats series", alert)
		}
		if strings.Contains(expr, "stats by") {
			t.Fatalf("canary alert %q has unbounded grouping", alert)
		}
	}

	diagnosticExpr := stringField(t, byAlert["LogPipelineDiagnosticCanaryMissing"], "expr")
	assertTextContains(t, diagnosticExpr, `guardian_log_pipeline_canary:="diagnostic_ok"`, path)
	assertTextContains(t, diagnosticExpr, "count_uniq(guardian_node) as nodes", path)
	assertTextContains(t, diagnosticExpr, "filter nodes:<3", path)
	markerExpr := stringField(t, byAlert["LogSanitizerCanaryMissing"], "expr")
	for _, want := range []string{
		`guardian_log_sanitizer_canary:="api-key=[REDACTED]"`,
		"guardian_node:*",
		`guardian_redacted:="true"`,
		`guardian_redaction_rule:="sensitive_field"`,
		"count_uniq(guardian_node) as nodes",
		"filter nodes:<3",
	} {
		assertTextContains(t, markerExpr, want, path)
	}

	leakRule := byAlert["LogSanitizerCanaryCredentialLeaked"]
	leakExpr := stringField(t, leakRule, "expr")
	for _, want := range []string{
		"pack_json as guardian_all_fields",
		"guardian_all_fields:*" + fixture + "*",
	} {
		assertTextContains(t, leakExpr, want, path)
	}
	annotations, err := yaml.Marshal(leakRule["annotations"])
	if err != nil {
		t.Fatalf("marshal leak-rule annotations: %v", err)
	}
	assertTextNotContains(t, string(annotations), fixture, path)

	metricsRule := findDoc(t, docs, "VMRule", "log-pipeline-health")
	metricsText, err := yaml.Marshal(metricsRule)
	if err != nil {
		t.Fatalf("marshal Fluent Bit metrics rules: %v", err)
	}
	for _, want := range []string{
		"fluentbit_output_errors_total",
		"fluentbit_output_retries_total",
		"fluentbit_output_retries_failed_total",
		"fluentbit_output_dropped_records_total",
		"fluentbit_filter_drop_records_total",
		`name="guardian_redactor"`,
		"count by (pod)",
		"vmalert_config_last_reload_successful",
		"vmalert_alerting_rules_errors_total",
		"vmalert_alerts_send_errors_total",
		`pod=~"vmalert-vlogs.*"`,
	} {
		assertTextContains(t, string(metricsText), want, path)
	}

	kustomizationPath := runfilePath("src/infrastructure/deployments/alerting/kustomization.yaml")
	kustomization := singleYAMLDoc(t, kustomizationPath)
	foundManifest := false
	for _, resource := range sliceValue(nestedValue(t, kustomization, "resources")) {
		if stringValue(resource) == "log-pipeline-health.yaml" {
			foundManifest = true
		}
	}
	if !foundManifest {
		t.Fatalf("%s does not include log-pipeline-health.yaml", kustomizationPath)
	}

	vmalertPath := runfilePath("src/infrastructure/deployments/alerting/vmalert-vlogs.yaml")
	vmalert := findDoc(t, yamlDocs(t, vmalertPath), "VMAlert", "vmalert-vlogs")
	configMaps := sliceValue(nestedValue(t, vmalert, "spec", "configMaps"))
	rulePaths := sliceValue(nestedValue(t, vmalert, "spec", "rulePath"))
	configMapMatches, rulePathMatches := 0, 0
	for _, configMap := range configMaps {
		if stringValue(configMap) == "guardian-log-pipeline-vlogs-rules" {
			configMapMatches++
		}
	}
	for _, rulePath := range rulePaths {
		if stringValue(rulePath) == "/etc/vm/configs/guardian-log-pipeline-vlogs-rules/*.yaml" {
			rulePathMatches++
		}
	}
	if configMapMatches != 1 || rulePathMatches != 1 {
		t.Fatalf("vmalert-vlogs health rule wiring: configMap matches=%d rulePath matches=%d, want one each", configMapMatches, rulePathMatches)
	}
}

func stringField(t *testing.T, value map[string]interface{}, field string) string {
	t.Helper()
	if value == nil {
		t.Fatalf("rule is missing while reading %q", field)
	}
	got, ok := value[field].(string)
	if !ok {
		t.Fatalf("rule field %q is not a string", field)
	}
	return got
}
