package main

import (
	"encoding/base64"
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
		Kubectl:         "/kubectl",
		Namespace:       "tenant-root",
		StatefulSet:     "openbao-guardian",
		BootstrapSecret: "openbao-guardian-bootstrap",
		Mode:            "snapshot",
		SnapshotName:    "guardian-openbao-test.snap",
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
}

func TestParseInitResult(t *testing.T) {
	got, err := parseInitResult(`{"unseal_keys_b64":["abc"],"root_token":"root"}`)
	if err != nil {
		t.Fatalf("parseInitResult() error = %v", err)
	}
	if got.UnsealKey != "abc" || got.RootToken != "root" {
		t.Fatalf("parseInitResult() = %#v", got)
	}

	if _, err := parseInitResult(`{"unseal_keys_b64":[],"root_token":"root"}`); err == nil {
		t.Fatalf("missing unseal key accepted")
	}
	if _, err := parseInitResult(`{"unseal_keys_b64":["abc"],"root_token":""}`); err == nil {
		t.Fatalf("missing root token accepted")
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

	init, err := parseInitResult("warning\n{\"unseal_keys_b64\":[\"abc\"],\"root_token\":\"root\"}\n")
	if err != nil {
		t.Fatalf("parseInitResult() wrapped output error = %v", err)
	}
	if init.UnsealKey != "abc" || init.RootToken != "root" {
		t.Fatalf("parseInitResult() wrapped output = %#v", init)
	}
}

func TestBootstrapSecretManifest(t *testing.T) {
	material := bootstrapMaterial{UnsealKey: "unseal-value", RootToken: "root-value"}
	got := bootstrapSecretManifest("tenant-root", "openbao-guardian-bootstrap", material)
	for _, want := range []string{
		"kind: Secret\nmetadata:\n  name: openbao-guardian-bootstrap\n  namespace: tenant-root\n",
		"guardian.dev/secret-scope: openbao-bootstrap\n",
		"unseal-key: " + base64.StdEncoding.EncodeToString([]byte(material.UnsealKey)) + "\n",
		"root-token: " + base64.StdEncoding.EncodeToString([]byte(material.RootToken)) + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bootstrap secret manifest missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, material.UnsealKey) || strings.Contains(got, material.RootToken) {
		t.Fatalf("bootstrap secret manifest contains raw secret material:\n%s", got)
	}
}

func TestPodNameAndShellQuote(t *testing.T) {
	if got, want := podName("openbao-guardian", 2), "openbao-guardian-2"; got != want {
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
		namespace:      "tenant-root",
	}.baseArgs("get", "pods")
	want := []string{"--kubeconfig", "/tmp/kubeconfig", "--request-timeout=5s", "-n", "tenant-root", "get", "pods"}
	if len(got) != len(want) {
		t.Fatalf("baseArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("baseArgs[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
}
