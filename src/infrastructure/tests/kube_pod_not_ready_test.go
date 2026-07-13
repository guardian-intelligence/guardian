package tests

import (
	"strings"
	"testing"
)

func TestKubePodNotReadyExcludesTerminalPods(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/monitoring-agents-kube-pod-not-ready.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "VMRule", "kind")
	assertNestedString(t, patch, "alerts-kubernetes-apps", "metadata", "name")
	assertNestedString(t, patch, "cozy-monitoring", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	groups := sliceValue(nestedValue(t, patch, "spec", "groups"))
	if len(groups) != 1 {
		t.Fatalf("spec.groups has %d entries, want the complete kubernetes-apps group", len(groups))
	}
	group := mapValue(groups[0])
	assertNestedString(t, group, "kubernetes-apps", "name")

	wantAlerts := map[string]bool{
		"KubePodCrashLooping":               true,
		"KubePodNotReady":                   true,
		"KubeDeploymentGenerationMismatch":  true,
		"KubeDeploymentReplicasMismatch":    true,
		"KubeDeploymentRolloutStuck":        true,
		"KubeStatefulSetReplicasMismatch":   true,
		"KubeStatefulSetGenerationMismatch": true,
		"KubeStatefulSetUpdateNotRolledOut": true,
		"KubeDaemonSetRolloutStuck":         true,
		"KubeContainerWaiting":              true,
		"KubeDaemonSetNotScheduled":         true,
		"KubeDaemonSetMisScheduled":         true,
		"KubeJobNotCompleted":               true,
		"KubeJobFailed":                     true,
		"KubeHpaReplicasMismatch":           true,
		"KubeHpaMaxedOut":                   true,
	}
	rules := sliceValue(nestedValue(t, group, "rules"))
	if len(rules) != len(wantAlerts) {
		t.Fatalf("kubernetes-apps has %d rules, want all %d chart rules", len(rules), len(wantAlerts))
	}

	seen := make(map[string]bool, len(rules))
	var notReady map[string]interface{}
	var jobFailed map[string]interface{}
	for _, raw := range rules {
		rule := mapValue(raw)
		alert, ok := rule["alert"].(string)
		if !ok || !wantAlerts[alert] {
			t.Fatalf("unexpected kubernetes-apps alert %q", alert)
		}
		if seen[alert] {
			t.Fatalf("duplicate kubernetes-apps alert %q", alert)
		}
		seen[alert] = true
		if alert == "KubePodNotReady" {
			notReady = rule
		}
		if alert == "KubeJobFailed" {
			jobFailed = rule
		}
	}
	if notReady == nil {
		t.Fatal("KubePodNotReady rule is missing")
	}
	assertNestedString(t, notReady, "15m", "for")
	expr, ok := nestedValue(t, notReady, "expr").(string)
	if !ok {
		t.Fatal("KubePodNotReady expression is not a string")
	}
	if !strings.Contains(expr, `phase=~"Pending|Unknown"`) {
		t.Fatalf("KubePodNotReady does not target recoverable pod phases: %s", expr)
	}
	if strings.Contains(expr, "Failed") {
		t.Fatalf("KubePodNotReady includes terminal Failed pods: %s", expr)
	}
	if jobFailed == nil {
		t.Fatal("KubeJobFailed rule is missing")
	}
	jobExpr := stringValue(jobFailed["expr"])
	for _, want := range []string{
		`kube_job_owner{job="kube-state-metrics", owner_kind="CronJob"}`,
		"topk by (namespace, owner_name, cluster)",
		`label_replace(`,
		`"job_name", "$1", "owner_name"`,
	} {
		if !strings.Contains(jobExpr, want) {
			t.Fatalf("KubeJobFailed does not select the latest CronJob run with %q: %s", want, jobExpr)
		}
	}
}
