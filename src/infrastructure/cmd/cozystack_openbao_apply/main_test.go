package main

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	cfg := applyConfig{
		Kubectl:              "/kubectl",
		Tofu:                 "/tofu",
		Namespace:            "tenant-guardian-kms",
		StatefulSet:          "openbao-guardian",
		Service:              "openbao-guardian",
		BootstrapSecret:      "openbao-guardian-bootstrap",
		Root:                 "/repo/src/infrastructure/clusters/ash/bootstrap/opentofu/openbao-bootstrap",
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

func TestDecodeRootToken(t *testing.T) {
	want := "root-token"
	got, err := decodeRootToken(base64.StdEncoding.EncodeToString([]byte(want)))
	if err != nil {
		t.Fatalf("decodeRootToken() error = %v", err)
	}
	if got != want {
		t.Fatalf("decodeRootToken() = %q, want %q", got, want)
	}
	if _, err := decodeRootToken(""); err == nil {
		t.Fatalf("empty token accepted")
	}
	if _, err := decodeRootToken("not base64"); err == nil {
		t.Fatalf("invalid base64 accepted")
	}
}

func TestTofuArgs(t *testing.T) {
	root := "/repo/src/infrastructure/clusters/ash/bootstrap/opentofu/openbao-bootstrap"
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

func TestValidateBackendCredentials(t *testing.T) {
	if err := validateBackendCredentials([]string{
		"AWS_ACCESS_KEY_ID=access-key",
		"AWS_SECRET_ACCESS_KEY=secret-key",
	}); err != nil {
		t.Fatalf("valid backend credentials rejected: %v", err)
	}

	err := validateBackendCredentials([]string{"AWS_ACCESS_KEY_ID=access-key"})
	if err == nil {
		t.Fatalf("missing secret access key accepted")
	}
	if !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("missing credential error did not name AWS_SECRET_ACCESS_KEY: %v", err)
	}
	if !strings.Contains(err.Error(), "cloudflare_r2_access_key_id/cloudflare_r2_secret_access_key") {
		t.Fatalf("missing credential error did not explain R2 source names: %v", err)
	}

	err = validateBackendCredentials([]string{"AWS_ACCESS_KEY_ID=", "AWS_SECRET_ACCESS_KEY=secret-key"})
	if err == nil {
		t.Fatalf("empty access key accepted")
	}
	if !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") {
		t.Fatalf("empty credential error did not name AWS_ACCESS_KEY_ID: %v", err)
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
		namespace:      "tenant-guardian-kms",
	}
	want := []string{"--kubeconfig", "/kubeconfig", "--request-timeout=15s", "-n", "tenant-guardian-kms", "get", "pods"}
	if got := runner.args("get", "pods"); !reflect.DeepEqual(got, want) {
		t.Fatalf("kubectl args = %#v, want %#v", got, want)
	}
}

func TestOpenBaoPortForwardArgs(t *testing.T) {
	runner := kubectlRunner{
		kubeconfig:     "/kubeconfig",
		requestTimeout: "15s",
		namespace:      "tenant-guardian-kms",
	}
	want := []string{
		"--kubeconfig", "/kubeconfig",
		"--request-timeout=15s",
		"-n", "tenant-guardian-kms",
		"port-forward",
		"--address", "127.0.0.1",
		"svc/openbao-guardian",
		"18200:8200",
	}
	if got := openBaoPortForwardArgs(runner, "openbao-guardian", 18200); !reflect.DeepEqual(got, want) {
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
