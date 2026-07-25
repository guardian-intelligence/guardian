package tests

import (
	"strings"
	"testing"
)

// The hubble metrics endpoint must be scraped exactly once. Two scrapes of
// the same hostPort produce identical duplicate series, and every drop query
// that sums without a job selector — including the alert rule below — reads
// double the real rate.
func TestHubbleMetricsAreScrapedExactlyOnce(t *testing.T) {
	hubble := singleYAMLDoc(t, runfilePath("src/infrastructure/base/platform-patches/cozystack-networking-hubble.yaml"))
	metrics := nestedMap(t, hubble, "spec", "components", "cilium", "values", "cilium", "hubble", "metrics")
	if enabled := nestedMap(t, metrics, "serviceMonitor"); enabled["enabled"] != true {
		t.Fatal("hubble.metrics.serviceMonitor is disabled; the chart scrape is the only scrape of the agents")
	}

	for _, doc := range yamlDocs(t, runfilePath("src/infrastructure/deployments/alerting/cilium-drop-metrics.yaml")) {
		switch stringValue(doc["kind"]) {
		case "VMPodScrape", "VMServiceScrape", "PodMonitor", "ServiceMonitor":
			t.Fatalf("cilium-drop-metrics.yaml ships a %s; the chart's serviceMonitor already scrapes hubble", stringValue(doc["kind"]))
		}
	}
}

// hubble_drop_total carries only reason and protocol unless the drop handler
// is given context options, which is what made a standing POLICY_DENIED
// baseline unattributable. Pod- and workload-scoped contexts are deliberately
// excluded: a Job pod resolves to its timestamped Job name, so CronJobs on
// minute schedules would mint a new series per run.
func TestHubbleDropMetricCarriesBoundedAttribution(t *testing.T) {
	hubble := singleYAMLDoc(t, runfilePath("src/infrastructure/base/platform-patches/cozystack-networking-hubble.yaml"))
	enabled := nestedStringSlice(t, nestedMap(t, hubble, "spec", "components", "cilium", "values", "cilium", "hubble", "metrics"), "enabled")

	var drop string
	for _, metric := range enabled {
		if metric == "drop" || strings.HasPrefix(metric, "drop:") {
			drop = metric
		}
	}
	if drop == "" {
		t.Fatal("the drop metric is not enabled; policy denials would be invisible")
	}
	for _, want := range []string{
		"sourceContext=namespace|reserved-identity",
		"destinationContext=namespace|reserved-identity",
		"labelsContext=traffic_direction",
	} {
		if !strings.Contains(drop, want) {
			t.Fatalf("drop metric %q is missing %q; denials would page without attribution", drop, want)
		}
	}
	for _, forbidden := range []string{"=pod", "|pod", "workload"} {
		if strings.Contains(drop, forbidden) {
			t.Fatalf("drop metric %q uses %q; Job-name churn makes that context unbounded", drop, forbidden)
		}
	}
}

// One denied packet advances hubble_drop_total twice: Cilium emits both a
// DropNotify and a PolicyVerdictNotify, and hubble's drop handler counts
// every flow whose verdict is DROPPED. A threshold written as if the counter
// tracked packets understates what it takes to page by half.
func TestCiliumDropAlertThresholdIsStatedInNotifications(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/alerting/cilium-drop-metrics.yaml")
	var rules []interface{}
	for _, doc := range yamlDocs(t, path) {
		if stringValue(doc["kind"]) != "VMRule" {
			continue
		}
		for _, group := range sliceValue(nestedValue(t, doc, "spec", "groups")) {
			rules = append(rules, sliceValue(mapValue(group)["rules"])...)
		}
	}

	found := false
	for _, raw := range rules {
		rule := mapValue(raw)
		if stringValue(rule["alert"]) != "CiliumPolicyDeniedSustained" {
			continue
		}
		found = true
		if got := strings.TrimSpace(stringValue(rule["expr"])); !strings.HasSuffix(got, "> 0.1") {
			t.Fatalf("CiliumPolicyDeniedSustained expr = %q, want a 0.1 notifications/s (0.05 packets/s) floor", got)
		}
		description := stringValue(nestedValue(t, rule, "annotations", "description"))
		assertTextContains(t, description, "sum by (source, destination, traffic_direction, protocol)", path)
	}
	if !found {
		t.Fatal("CiliumPolicyDeniedSustained is missing")
	}
}

// Debugging a policy denial means reading the policies. The built-in view
// ClusterRole does not cover cilium.io, so without this the agent can page on
// a denial it is structurally unable to explain.
func TestPlatformAgentCanReadCiliumPolicies(t *testing.T) {
	path := runfilePath("src/infrastructure/base/cozystack/platform-admins.yaml")
	raw := readText(t, path)
	start := strings.Index(raw, "name: guardian-persona-cluster-view")
	if start < 0 {
		t.Fatal("guardian-persona-cluster-view is missing")
	}
	clusterView := raw[start:]
	if end := strings.Index(clusterView, "\n---"); end > 0 {
		clusterView = clusterView[:end]
	}
	for _, want := range []string{
		"- cilium.io",
		"- ciliumclusterwidenetworkpolicies",
		"- ciliumendpoints",
		"- ciliumidentities",
		"- ciliumnetworkpolicies",
		"- ciliumnodes",
	} {
		assertTextContains(t, clusterView, want, path)
	}
	for _, forbidden := range []string{"- create", "- delete", "- update", "- patch"} {
		assertTextNotContains(t, clusterView, forbidden, path)
	}
}
