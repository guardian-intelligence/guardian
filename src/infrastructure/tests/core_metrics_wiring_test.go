package tests

import (
	"strings"
	"testing"
)

func TestCoreMetricsWiringConformance(t *testing.T) {
	talmValuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	talmValues := singleYAMLDoc(t, talmValuesPath)
	assertNestedString(t, talmValues, "http://127.0.0.1:2381", "etcd", "metricsListenURL")
	assertNestedString(t, talmValues, "extensive", "etcd", "metricsLevel")
	talmTemplatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	talmTemplate := readText(t, talmTemplatePath)
	assertTextContains(t, talmTemplate, "etcd.metricsListenURL", talmTemplatePath)
	assertTextContains(t, talmTemplate, "etcd.metricsLevel", talmTemplatePath)
	assertTextNotContains(t, talmTemplate, "http://127.0.0.1:2381", talmTemplatePath)
	assertTextNotContains(t, talmTemplate, "metrics: extensive", talmTemplatePath)

	packagePath := runfilePath("src/infrastructure/base/platform-patches/cozystack-monitoring-agents.yaml")
	monitoringPackage := singleYAMLDoc(t, packagePath)
	values := nestedMap(t, monitoringPackage, "spec", "components", "monitoring-agents", "values")
	assertNestedBool(t, values, true, "scrapeRules", "etcd", "enabled")
	assertNestedBool(t, values, true, "kube-state-metrics", "selfMonitor", "enabled")

	vmagentPath := runfilePath("src/infrastructure/base/app-patches/monitoring-agents-vmagent-resources.yaml")
	vmagent := readText(t, vmagentPath)
	for _, want := range []string{
		"inlineRelabelConfig:",
		"sourceLabels: [job, metrics_path]",
		"regex: cadvisor;/metrics/cadvisor",
		"replacement: kubelet",
	} {
		assertTextContains(t, vmagent, want, vmagentPath)
	}

	wiringPath := runfilePath("src/infrastructure/deployments/alerting/core-metrics-wiring.yaml")
	docs := yamlDocs(t, wiringPath)
	scrape := findDoc(t, docs, "VMServiceScrape", "kube-state-metrics-self")
	assertNestedString(t, scrape, "cozy-monitoring", "metadata", "namespace")
	presence := findDoc(t, docs, "VMRule", "cozystack-core-metrics-presence")
	assertNestedString(t, presence, "tenant-root", "metadata", "namespace")

	rules := readText(t, wiringPath)
	for _, alert := range []string{
		"VMAgentDown",
		"VMAgentCrashLooping",
		"EtcdMetricsAbsent",
		"EtcdGRPCLatencyMetricsAbsent",
		"KubeStateMetricsSelfMetricsAbsent",
		"KubeStateObjectMetricsAbsent",
		"FluxResourceMetricsAbsent",
		"CadvisorRecordingsAbsent",
	} {
		assertTextContains(t, rules, "alert: "+alert, wiringPath)
	}

	groups := sliceValue(nestedValue(t, presence, "spec", "groups"))
	var vmagentDown map[string]interface{}
	for _, rawGroup := range groups {
		for _, rawRule := range sliceValue(mapValue(rawGroup)["rules"]) {
			rule := mapValue(rawRule)
			if rule["alert"] == "VMAgentDown" {
				vmagentDown = rule
			}
		}
	}
	if vmagentDown == nil {
		t.Fatal("VMAgentDown rule is missing")
	}
	assertNestedString(t, vmagentDown, "5m", "for")
	assertNestedString(t, vmagentDown, "15m", "keep_firing_for")
	assertNestedString(t, vmagentDown, "critical", "labels", "severity")
	if expr := stringValue(vmagentDown["expr"]); !strings.Contains(expr, `absent(up{namespace="cozy-monitoring",job="vmagent-vmagent"})`) {
		t.Fatalf("VMAgentDown expression does not watch the self-scrape: %s", expr)
	}

	var vmagentCrashLooping map[string]interface{}
	for _, rawGroup := range groups {
		for _, rawRule := range sliceValue(mapValue(rawGroup)["rules"]) {
			rule := mapValue(rawRule)
			if rule["alert"] == "VMAgentCrashLooping" {
				vmagentCrashLooping = rule
			}
		}
	}
	if vmagentCrashLooping == nil {
		t.Fatal("VMAgentCrashLooping rule is missing")
	}
	assertNestedString(t, vmagentCrashLooping, "5m", "for")
	assertNestedString(t, vmagentCrashLooping, "15m", "keep_firing_for")
	assertNestedString(t, vmagentCrashLooping, "critical", "labels", "severity")
	crashLoopExpr := stringValue(vmagentCrashLooping["expr"])
	for _, want := range []string{
		"kube_pod_container_status_restarts_total",
		`namespace="cozy-monitoring"`,
		`container="vmagent"`,
		"[15m]",
		"> 2",
	} {
		if !strings.Contains(crashLoopExpr, want) {
			t.Fatalf("VMAgentCrashLooping expression is missing %q: %s", want, crashLoopExpr)
		}
	}
}

func TestVMAgentHasColdStartMemoryHeadroom(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/monitoring-agents-vmagent-resources.yaml")
	vmagent := singleYAMLDoc(t, path)
	assertNestedString(t, vmagent, "512Mi", "spec", "resources", "requests", "memory")
	assertNestedString(t, vmagent, "1Gi", "spec", "resources", "limits", "memory")
}
