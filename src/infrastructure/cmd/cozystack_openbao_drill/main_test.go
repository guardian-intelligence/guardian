package main

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultSnapshotName(t *testing.T) {
	got := defaultSnapshotName(time.Date(2026, 6, 22, 15, 16, 17, 0, time.UTC))
	want := "guardian-openbao-20260622t151617z.snap"
	if got != want {
		t.Fatalf("defaultSnapshotName() = %q, want %q", got, want)
	}
}

func TestValidateConfig(t *testing.T) {
	cfg := openBaoConfig{
		Kubectl:      "/kubectl",
		Namespace:    "tenant-guardian",
		StatefulSet:  "guardian-openbao",
		Mode:         "snapshot",
		SnapshotName: "guardian-openbao-test.snap",
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	badMode := cfg
	badMode.Mode = "restore"
	if err := validateConfig(badMode); err == nil {
		t.Fatalf("invalid mode accepted")
	}

	badSnapshot := cfg
	badSnapshot.SnapshotName = "../snapshot"
	if err := validateConfig(badSnapshot); err == nil {
		t.Fatalf("path traversal snapshot accepted")
	}

	for _, name := range []string{
		"snapshot name.snap",
		"snapshot;rm.snap",
		"/tmp/snapshot.snap",
		strings.Repeat("a", 129) + ".snap",
	} {
		t.Run("bad snapshot "+name, func(t *testing.T) {
			bad := cfg
			bad.SnapshotName = name
			if err := validateConfig(bad); err == nil {
				t.Fatalf("invalid snapshot name %q accepted", name)
			}
		})
	}
}

func TestParseNativeJSONWithWrapperOutput(t *testing.T) {
	status, err := parseBaoStatus("warning from wrapper\n{\"initialized\":true,\"sealed\":false}\n")
	if err != nil {
		t.Fatalf("parseBaoStatus() error = %v", err)
	}
	if !status.Initialized || status.Sealed {
		t.Fatalf("parseBaoStatus() = %#v", status)
	}
	if !looksLikeBaoStatusJSON("warning\n{\"initialized\":false,\"sealed\":true}\n") {
		t.Fatalf("looksLikeBaoStatusJSON() rejected status payload")
	}
}

func TestRootTokenFromEnvPrefersBaoToken(t *testing.T) {
	t.Setenv("BAO_TOKEN", "bao-token")
	t.Setenv("VAULT_TOKEN", "vault-token")

	got, source, err := rootTokenFromEnv()
	if err != nil {
		t.Fatalf("rootTokenFromEnv() error = %v", err)
	}
	if got != "bao-token" {
		t.Fatalf("rootTokenFromEnv() token = %q, want BAO_TOKEN value", got)
	}
	if source != "BAO_TOKEN" {
		t.Fatalf("rootTokenFromEnv() source = %q, want BAO_TOKEN", source)
	}
}

func TestRootTokenFromEnvUsesVaultEnv(t *testing.T) {
	t.Setenv("BAO_TOKEN", "")
	t.Setenv("VAULT_TOKEN", "vault-token")

	got, source, err := rootTokenFromEnv()
	if err != nil {
		t.Fatalf("rootTokenFromEnv() error = %v", err)
	}
	if got != "vault-token" {
		t.Fatalf("rootTokenFromEnv() token = %q, want VAULT_TOKEN value", got)
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

func TestPodNameAndShellQuote(t *testing.T) {
	if got, want := podName("guardian-openbao", 2), "guardian-openbao-2"; got != want {
		t.Fatalf("podName() = %q, want %q", got, want)
	}
	if got, want := shellQuote("a'b"), `'a'"'"'b'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestKubectlBaseArgs(t *testing.T) {
	got := kubectlRunner{
		kubeconfig:     "/tmp/kubeconfig",
		requestTimeout: "5s",
		namespace:      "tenant-guardian",
	}.baseArgs("get", "pods")
	want := []string{"--kubeconfig", "/tmp/kubeconfig", "--request-timeout=5s", "-n", "tenant-guardian", "get", "pods"}
	if len(got) != len(want) {
		t.Fatalf("baseArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("baseArgs[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
}
