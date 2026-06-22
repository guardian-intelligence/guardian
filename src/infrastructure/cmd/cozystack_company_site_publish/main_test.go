package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	cfg := publishConfig{
		Kubectl:        "/kubectl",
		RequestTimeout: "5s",
		Bazel:          "bazelisk",
		Target:         "//src/products/company/site:push-harbor",
		Namespace:      "tenant-root",
		Secret:         "harbor-guardian-credentials",
		Host:           "harbor.guardianintelligence.org",
		Workspace:      ".",
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	badTarget := cfg
	badTarget.Target = "src/products/company/site:push-harbor"
	if err := validateConfig(badTarget); err == nil {
		t.Fatalf("relative target accepted")
	}

	badHost := cfg
	badHost.Host = "Harbor.Example"
	if err := validateConfig(badHost); err == nil {
		t.Fatalf("invalid host accepted")
	}
}

func TestKubectlArgs(t *testing.T) {
	got := kubectlArgs(publishConfig{
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

func TestDockerConfigPayload(t *testing.T) {
	raw, err := dockerConfigPayload("harbor.guardianintelligence.org", "admin", "secret")
	if err != nil {
		t.Fatalf("dockerConfigPayload() error = %v", err)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("docker config payload contains raw password:\n%s", raw)
	}

	var parsed dockerConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse docker config: %v", err)
	}
	auth := parsed.Auths["harbor.guardianintelligence.org"].Auth
	want := base64.StdEncoding.EncodeToString([]byte("admin:secret"))
	if auth != want {
		t.Fatalf("auth = %q, want %q", auth, want)
	}
}

func TestRedactSecret(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("admin:secret"))
	got := redactSecret("password secret auth "+auth, "secret")
	if strings.Contains(got, "secret") || strings.Contains(got, auth) {
		t.Fatalf("redactSecret leaked secret: %q", got)
	}
	if !strings.Contains(got, "<redacted>") || !strings.Contains(got, "<redacted-auth>") {
		t.Fatalf("redactSecret missing markers: %q", got)
	}
}
