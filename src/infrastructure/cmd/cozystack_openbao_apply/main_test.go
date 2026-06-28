package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	cfg := applyConfig{
		Kubectl:              "/kubectl",
		Tofu:                 "/tofu",
		Namespace:            "tenant-guardian",
		StatefulSet:          "guardian-openbao",
		Service:              "guardian-openbao",
		Root:                 "/repo/src/infrastructure/bootstrap/guardian-mgmt-openbao",
		BackendEndpoint:      "https://account.r2.cloudflarestorage.com",
		Mode:                 "apply",
		PortForwardReadyWait: time.Second,
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	badMode := cfg
	badMode.Mode = "destroy"
	if err := validateConfig(badMode); err == nil {
		t.Fatalf("invalid mode accepted")
	}

	badService := cfg
	badService.Service = "OpenBao"
	if err := validateConfig(badService); err == nil {
		t.Fatalf("invalid service accepted")
	}

	missingEndpoint := cfg
	missingEndpoint.BackendEndpoint = ""
	if err := validateConfig(missingEndpoint); err == nil {
		t.Fatalf("missing backend endpoint accepted")
	}
}

func TestRootTokenFromEnvPrefersBaoToken(t *testing.T) {
	t.Setenv("BAO_TOKEN", "env-token")
	t.Setenv("VAULT_TOKEN", "vault-token")

	got, source, err := rootTokenFromEnv()
	if err != nil {
		t.Fatalf("rootTokenFromEnv() error = %v", err)
	}
	if got != "env-token" {
		t.Fatalf("rootTokenFromEnv() token = %q, want env-token", got)
	}
	if source != "BAO_TOKEN" {
		t.Fatalf("rootTokenFromEnv() source = %q, want BAO_TOKEN", source)
	}
}

func TestRootTokenFromEnvFallsBackToVaultToken(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "vault-token")

	got, source, err := rootTokenFromEnv()
	if err != nil {
		t.Fatalf("rootTokenFromEnv() error = %v", err)
	}
	if got != "vault-token" {
		t.Fatalf("rootTokenFromEnv() token = %q, want vault-token", got)
	}
	if source != "VAULT_TOKEN" {
		t.Fatalf("rootTokenFromEnv() source = %q, want VAULT_TOKEN", source)
	}
}

func TestRootTokenFromEnvRequiresEnv(t *testing.T) {
	t.Setenv("BAO_TOKEN", "")
	t.Setenv("VAULT_TOKEN", "")

	if _, _, err := rootTokenFromEnv(); err == nil {
		t.Fatalf("rootTokenFromEnv() accepted missing root token")
	}
}

func TestTofuArgs(t *testing.T) {
	root := "/repo/src/infrastructure/bootstrap/guardian-mgmt-openbao"
	endpoint := "https://account.r2.cloudflarestorage.com"
	addr := "http://127.0.0.1:18200"

	wantInit := []string{
		"-chdir=" + root,
		"init",
		"-input=false",
		"-reconfigure",
		"-backend-config=endpoint=" + endpoint,
	}
	if got := tofuInitArgs(root, endpoint); !reflect.DeepEqual(got, wantInit) {
		t.Fatalf("tofuInitArgs() = %#v, want %#v", got, wantInit)
	}

	wantApply := []string{
		"-chdir=" + root,
		"apply",
		"-input=false",
		"-var=openbao_addr=" + addr,
		"-auto-approve",
	}
	if got := tofuRunArgs("apply", root, addr); !reflect.DeepEqual(got, wantApply) {
		t.Fatalf("tofuRunArgs(apply) = %#v, want %#v", got, wantApply)
	}

	wantPlan := []string{
		"-chdir=" + root,
		"plan",
		"-input=false",
		"-var=openbao_addr=" + addr,
	}
	if got := tofuRunArgs("plan", root, addr); !reflect.DeepEqual(got, wantPlan) {
		t.Fatalf("tofuRunArgs(plan) = %#v, want %#v", got, wantPlan)
	}
}

func TestTofuEnv(t *testing.T) {
	got := tofuEnv([]string{"VAULT_TOKEN=old", "KEEP=yes"}, "http://127.0.0.1:18200", "root-token")
	for _, want := range []string{
		"KEEP=yes",
		"BAO_ADDR=http://127.0.0.1:18200",
		"BAO_TOKEN=root-token",
		"VAULT_ADDR=http://127.0.0.1:18200",
		"VAULT_TOKEN=root-token",
		"VAULT_CLIENT_TIMEOUT=120s",
		"AWS_EC2_METADATA_DISABLED=true",
	} {
		if !hasExactEnv(got, want) {
			t.Fatalf("tofuEnv missing %q from %#v", want, got)
		}
	}
}

func TestRedactToken(t *testing.T) {
	got := redactToken("token=root-token\nagain root-token", "root-token")
	if strings.Contains(got, "root-token") {
		t.Fatalf("token was not redacted: %q", got)
	}
	if strings.Count(got, "<redacted>") != 2 {
		t.Fatalf("redacted output = %q", got)
	}
}

func TestKubectlArgs(t *testing.T) {
	runner := kubectlRunner{
		kubeconfig:     "/kubeconfig",
		requestTimeout: "15s",
		namespace:      "tenant-guardian",
	}
	want := []string{"--kubeconfig", "/kubeconfig", "--request-timeout=15s", "-n", "tenant-guardian", "get", "pods"}
	if got := runner.args("get", "pods"); !reflect.DeepEqual(got, want) {
		t.Fatalf("kubectl args = %#v, want %#v", got, want)
	}
}

func TestOpenBaoPortForwardArgs(t *testing.T) {
	runner := kubectlRunner{
		kubeconfig:     "/kubeconfig",
		requestTimeout: "15s",
		namespace:      "tenant-guardian",
	}
	want := []string{
		"--kubeconfig", "/kubeconfig",
		"--request-timeout=15s",
		"-n", "tenant-guardian",
		"port-forward",
		"--address", "127.0.0.1",
		"svc/guardian-openbao",
		"18200:8200",
	}
	if got := openBaoPortForwardArgs(runner, "guardian-openbao", 18200); !reflect.DeepEqual(got, want) {
		t.Fatalf("openBaoPortForwardArgs() = %#v, want %#v", got, want)
	}
}

func hasExactEnv(env []string, item string) bool {
	for _, got := range env {
		if got == item {
			return true
		}
	}
	return false
}
