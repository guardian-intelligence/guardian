package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateOpenBaoCRsConverged(t *testing.T) {
	raw := `{"items":[
		{"kind":"OpenBaoPolicy","metadata":{"name":"ops-controller"},"status":{"conditions":[
			{"type":"Authenticated","status":"True","reason":"Applied"},
			{"type":"Applied","status":"True","reason":"Applied"},
			{"type":"Ready","status":"True","reason":"Applied"},
			{"type":"DriftDetected","status":"False","reason":"Applied"}
		]}}
	]}`
	if err := validateOpenBaoCRs(raw, []string{"OpenBaoPolicy/ops-controller"}); err != nil {
		t.Fatalf("validateOpenBaoCRs() error = %v", err)
	}
}

func TestValidateOpenBaoCRsRejectsSelfInitIncomplete(t *testing.T) {
	raw := `{"items":[
		{"kind":"OpenBaoPolicy","metadata":{"name":"ops-controller"},"status":{"lastError":"login failed: invalid role name","conditions":[
			{"type":"Authenticated","status":"False","reason":"SelfInitIncomplete"},
			{"type":"Applied","status":"False","reason":"SelfInitIncomplete"},
			{"type":"Ready","status":"False","reason":"SelfInitIncomplete"},
			{"type":"DriftDetected","status":"Unknown","reason":"SelfInitIncomplete"}
		]}}
	]}`
	err := validateOpenBaoCRs(raw, []string{"OpenBaoPolicy/ops-controller"})
	if err == nil {
		t.Fatalf("validateOpenBaoCRs() accepted self-init-incomplete CR as converged")
	}
	if !strings.Contains(err.Error(), "Authenticated = False") {
		t.Fatalf("validateOpenBaoCRs() error = %v, want condition detail", err)
	}
}

func TestValidateFluxKustomizations(t *testing.T) {
	raw := `{"items":[
		{"metadata":{"name":"guardian-system"},"status":{"lastAppliedRevision":"main@sha1:abc123","conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded"}]}}
	]}`
	if err := validateFluxKustomizations(raw, []string{"guardian-system"}, "abc123"); err != nil {
		t.Fatalf("validateFluxKustomizations() error = %v", err)
	}
}

func TestValidateFluxKustomizationsRequiresExpectedRevision(t *testing.T) {
	raw := `{"items":[
		{"metadata":{"name":"guardian-system"},"status":{"lastAppliedRevision":"main@sha1:old","conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded"}]}}
	]}`
	err := validateFluxKustomizations(raw, []string{"guardian-system"}, "new")
	if err == nil {
		t.Fatalf("validateFluxKustomizations() accepted stale revision")
	}
}

func TestValidateDeploymentReadyRequiresDigestPinnedManager(t *testing.T) {
	raw := `{"spec":{"template":{"spec":{"containers":[{"name":"manager","image":"example.com/openbao-ops-controller@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}},"status":{"readyReplicas":1,"replicas":1}}`
	if err := validateDeploymentReady(raw, "openbao-ops-controller"); err != nil {
		t.Fatalf("validateDeploymentReady() error = %v", err)
	}
}

func TestValidateDeploymentReplicasReady(t *testing.T) {
	raw := `{"status":{"readyReplicas":1,"replicas":1}}`
	if err := validateDeploymentReplicasReady(raw, "cert-manager", "cert-manager"); err != nil {
		t.Fatalf("validateDeploymentReplicasReady() error = %v", err)
	}
}

func TestValidateDeploymentReplicasReadyRejectsUnavailableDeployment(t *testing.T) {
	raw := `{"status":{"readyReplicas":0,"replicas":1}}`
	err := validateDeploymentReplicasReady(raw, "cert-manager", "cert-manager")
	if err == nil {
		t.Fatalf("validateDeploymentReplicasReady() accepted unavailable deployment")
	}
	if !strings.Contains(err.Error(), "readyReplicas=0") {
		t.Fatalf("validateDeploymentReplicasReady() error = %v, want readiness detail", err)
	}
}

func TestValidateReadyConditionRejectsFailedHelmRelease(t *testing.T) {
	raw := `{"status":{"conditions":[{"type":"Ready","status":"False","reason":"RollbackSucceeded"}]}}`
	err := validateReadyCondition(raw, "HelmRelease", "guardian-openbao")
	if err == nil {
		t.Fatalf("validateReadyCondition() accepted failed HelmRelease")
	}
	if !strings.Contains(err.Error(), "RollbackSucceeded") {
		t.Fatalf("validateReadyCondition() error = %v, want rollback reason", err)
	}
}

func TestValidateStatefulSetRolled(t *testing.T) {
	raw := `{"spec":{"replicas":3},"status":{"readyReplicas":3,"updatedReplicas":3,"currentRevision":"guardian-openbao-new","updateRevision":"guardian-openbao-new"}}`
	if err := validateStatefulSetRolled(raw, "guardian-openbao"); err != nil {
		t.Fatalf("validateStatefulSetRolled() error = %v", err)
	}
}

func TestValidateStatefulSetRolledAcceptsOnDeleteCurrentRevisionLag(t *testing.T) {
	raw := `{"spec":{"replicas":3,"updateStrategy":{"type":"OnDelete"}},"status":{"readyReplicas":3,"updatedReplicas":0,"currentRevision":"guardian-openbao-old","updateRevision":"guardian-openbao-new"}}`
	if err := validateStatefulSetRolled(raw, "guardian-openbao"); err != nil {
		t.Fatalf("validateStatefulSetRolled() error = %v", err)
	}
}

func TestValidateStatefulSetRolledRejectsRollingUpdateRevisionMismatch(t *testing.T) {
	raw := `{"spec":{"replicas":3,"updateStrategy":{"type":"RollingUpdate"}},"status":{"readyReplicas":3,"updatedReplicas":3,"currentRevision":"guardian-openbao-old","updateRevision":"guardian-openbao-new"}}`
	err := validateStatefulSetRolled(raw, "guardian-openbao")
	if err == nil {
		t.Fatalf("validateStatefulSetRolled() accepted revision mismatch")
	}
	if !strings.Contains(err.Error(), "currentRevision=") {
		t.Fatalf("validateStatefulSetRolled() error = %v, want revision detail", err)
	}
}

func TestValidateStatefulSetRolledRejectsMixedRevision(t *testing.T) {
	raw := `{"spec":{"replicas":3},"status":{"readyReplicas":2,"updatedReplicas":1,"currentRevision":"guardian-openbao-old","updateRevision":"guardian-openbao-new"}}`
	err := validateStatefulSetRolled(raw, "guardian-openbao")
	if err == nil {
		t.Fatalf("validateStatefulSetRolled() accepted mixed StatefulSet")
	}
	if !strings.Contains(err.Error(), "readyReplicas=2") {
		t.Fatalf("validateStatefulSetRolled() error = %v, want readiness detail", err)
	}
}

func TestKubectlRunnerArgs(t *testing.T) {
	runner := kubectlRunner{
		kubeconfig:     "/tmp/kubeconfig",
		kubeAPIServer:  "https://206.223.228.101:6443",
		requestTimeout: "10s",
	}
	got := runner.args("get", "nodes")
	want := []string{"--kubeconfig", "/tmp/kubeconfig", "--server", "https://206.223.228.101:6443", "--request-timeout", "10s", "get", "nodes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCSV(t *testing.T) {
	got := csv(" a, ,b,c ")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("csv() = %#v, want %#v", got, want)
	}
}
