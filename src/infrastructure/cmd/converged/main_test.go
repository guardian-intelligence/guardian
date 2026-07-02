package main

import (
	"reflect"
	"strings"
	"testing"
)

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

func TestValidateFluxKustomizationsRejectsMissingKustomization(t *testing.T) {
	raw := `{"items":[]}`
	err := validateFluxKustomizations(raw, []string{"guardian-system"}, "")
	if err == nil {
		t.Fatalf("validateFluxKustomizations() accepted missing Kustomization")
	}
	if !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("validateFluxKustomizations() error = %v, want missing detail", err)
	}
}

func TestValidateFluxKustomizationsSurfacesHealthCheckFailureMessage(t *testing.T) {
	raw := `{"items":[
		{"metadata":{"name":"guardian-openbao-ops-state"},"status":{"lastAppliedRevision":"main@sha1:abc123","conditions":[{"type":"Ready","status":"False","reason":"HealthCheckFailed","message":"OpenBaoPolicy/ops-controller status: 'InProgress'"}]}}
	]}`
	err := validateFluxKustomizations(raw, []string{"guardian-openbao-ops-state"}, "abc123")
	if err == nil {
		t.Fatalf("validateFluxKustomizations() accepted failed health check")
	}
	if !strings.Contains(err.Error(), "HealthCheckFailed") || !strings.Contains(err.Error(), "OpenBaoPolicy/ops-controller") {
		t.Fatalf("validateFluxKustomizations() error = %v, want reason and message detail", err)
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
