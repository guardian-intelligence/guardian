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
		WaitTimeout:    "15m",
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

	missingWait := cfg
	missingWait.WaitTimeout = ""
	if err := validateConfig(missingWait); err == nil {
		t.Fatalf("empty wait timeout accepted")
	}
}

func TestHarborReadinessChecks(t *testing.T) {
	cfg := publishConfig{
		Namespace:   "tenant-root",
		WaitTimeout: "20m",
	}
	got := harborReadinessChecks(cfg)
	requireCommand(t, got, "Harbor app yaml", "tenant-root", "harbors.apps.cozystack.io/guardian", "-o", "yaml")
	requireCommand(t, got, "Harbor registry bucket claim yaml", "tenant-root", "bucketclaims.objectstorage.k8s.io/harbor-guardian-registry", "-o", "yaml")
	requireCommand(t, got, "Harbor registry bucket access yaml", "tenant-root", "bucketaccesses.objectstorage.k8s.io/harbor-guardian-registry", "-o", "yaml")
	requireCommand(t, got, "wait Harbor app Ready", "--for=condition=Ready", "harbors.apps.cozystack.io/guardian", "--timeout=20m")
	requireCommand(t, got, "wait Harbor registry bucket ready", "--for=jsonpath={.status.bucketReady}=true", "bucketclaims.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=20m")
	requireCommand(t, got, "wait Harbor registry bucket access granted", "--for=jsonpath={.status.accessGranted}=true", "bucketaccesses.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=20m")
	requireCommand(t, got, "wait Harbor workloads Ready", "--for=condition=WorkloadsReady", "harbors.apps.cozystack.io/guardian", "--timeout=20m")
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

func requireCommand(t *testing.T, checks []kubectlCommand, label string, parts ...string) {
	t.Helper()
	for _, check := range checks {
		if check.Label != label {
			continue
		}
		for _, part := range parts {
			if !hasArg(check.Args, part) {
				t.Fatalf("%s missing arg %q: %#v", label, part, check.Args)
			}
		}
		return
	}
	t.Fatalf("missing command %q in %#v", label, checks)
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
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
