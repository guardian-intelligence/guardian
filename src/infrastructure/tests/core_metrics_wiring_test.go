package tests

import "testing"

func TestCoreMetricsWiringConformance(t *testing.T) {
	talmValuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	talmValues := singleYAMLDoc(t, talmValuesPath)
	assertNestedString(t, talmValues, "http://127.0.0.1:2381", "etcd", "metricsListenURL")
	talmTemplatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	talmTemplate := readText(t, talmTemplatePath)
	assertTextContains(t, talmTemplate, "etcd.metricsListenURL", talmTemplatePath)
	assertTextNotContains(t, talmTemplate, "http://127.0.0.1:2381", talmTemplatePath)

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
		"EtcdMetricsAbsent",
		"KubeStateMetricsSelfMetricsAbsent",
		"KubeStateObjectMetricsAbsent",
		"FluxResourceMetricsAbsent",
		"CadvisorRecordingsAbsent",
	} {
		assertTextContains(t, rules, "alert: "+alert, wiringPath)
	}
}
