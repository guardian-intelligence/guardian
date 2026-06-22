package main

import (
	"bytes"
	"testing"
	"time"
)

func TestStageResolution(t *testing.T) {
	tests := []struct {
		stage     string
		namespace string
		host      string
	}{
		{stage: "root", namespace: "tenant-root", host: "harbor.guardianintelligence.org"},
		{stage: "dev", namespace: "tenant-dev", host: "harbor.dev.gi.org"},
		{stage: "gamma", namespace: "tenant-gamma", host: "harbor.gamma.gi.org"},
		{stage: "prod", namespace: "tenant-prod", host: "harbor.prod.gi.org"},
	}
	for _, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			namespace, err := namespaceForStage(tt.stage)
			if err != nil {
				t.Fatalf("namespaceForStage(%q): %v", tt.stage, err)
			}
			if namespace != tt.namespace {
				t.Fatalf("namespace = %q, want %q", namespace, tt.namespace)
			}
			host, err := harborHost(tt.stage)
			if err != nil {
				t.Fatalf("harborHost(%q): %v", tt.stage, err)
			}
			if host != tt.host {
				t.Fatalf("host = %q, want %q", host, tt.host)
			}
		})
	}
}

func TestDefaultTag(t *testing.T) {
	got := defaultTag("gamma", time.Date(2026, 6, 22, 14, 15, 16, 0, time.UTC))
	want := "guardian-gamma-20260622t141516z"
	if got != want {
		t.Fatalf("defaultTag() = %q, want %q", got, want)
	}
}

func TestValidateConfig(t *testing.T) {
	base := harborRegistryConfig{
		Oras:         "/oras",
		Kubectl:      "/kubectl",
		Stage:        "dev",
		Namespace:    "tenant-dev",
		Host:         "harbor.dev.gi.org",
		Repository:   "library/guardian-smoke",
		Tag:          "guardian-dev-test",
		Iterations:   1,
		PayloadBytes: 128,
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	badRepo := base
	badRepo.Repository = "../not-valid"
	if err := validateConfig(badRepo); err == nil {
		t.Fatalf("invalid repository accepted")
	}

	badTag := base
	badTag.Tag = "-not-valid"
	if err := validateConfig(badTag); err == nil {
		t.Fatalf("invalid tag accepted")
	}

	badIterations := base
	badIterations.Iterations = 0
	if err := validateConfig(badIterations); err == nil {
		t.Fatalf("zero iterations accepted")
	}
}

func TestRegistryRef(t *testing.T) {
	cfg := harborRegistryConfig{
		Host:       "harbor.gamma.gi.org",
		Repository: "library/guardian-smoke",
		Tag:        "guardian-gamma-test",
		Iterations: 1,
	}
	if got, want := registryRef(cfg, 1), "harbor.gamma.gi.org/library/guardian-smoke:guardian-gamma-test"; got != want {
		t.Fatalf("registryRef single = %q, want %q", got, want)
	}
	cfg.Iterations = 2
	if got, want := registryRef(cfg, 2), "harbor.gamma.gi.org/library/guardian-smoke:guardian-gamma-test-000002"; got != want {
		t.Fatalf("registryRef multi = %q, want %q", got, want)
	}
}

func TestPayloadFor(t *testing.T) {
	cfg := harborRegistryConfig{
		Stage:        "dev",
		Host:         "harbor.dev.gi.org",
		Repository:   "library/guardian-smoke",
		PayloadBytes: 256,
	}
	got := payloadFor(cfg, 3)
	if len(got) != 256 {
		t.Fatalf("payload length = %d, want 256", len(got))
	}
	if !bytes.Contains(got, []byte("stage=dev")) || !bytes.Contains(got, []byte("iteration=3")) {
		t.Fatalf("payload missing identifying fields: %q", got)
	}
}

func TestKubectlArgs(t *testing.T) {
	got := kubectlArgs(harborRegistryConfig{
		Kubeconfig:     "/tmp/kubeconfig",
		RequestTimeout: "5s",
	}, "get", "nodes")
	want := []string{"--kubeconfig", "/tmp/kubeconfig", "--request-timeout=5s", "get", "nodes"}
	if len(got) != len(want) {
		t.Fatalf("kubectlArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kubectlArgs[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
}

func TestOrasBaseArgs(t *testing.T) {
	runner := orasRunner{
		registryConfig: "/tmp/oras-auth.json",
	}
	got := runner.baseArgs("push", "example.com/repo:tag")
	want := []string{"push", "example.com/repo:tag", "--registry-config", "/tmp/oras-auth.json"}
	if len(got) != len(want) {
		t.Fatalf("baseArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("baseArgs[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
	for _, arg := range got {
		if arg == "--plain-http" || arg == "--insecure" {
			t.Fatalf("baseArgs includes insecure bypass %q: %#v", arg, got)
		}
	}
}
