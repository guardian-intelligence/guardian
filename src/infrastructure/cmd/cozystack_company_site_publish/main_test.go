package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	cfg := publishConfig{
		Kubectl:                 "/kubectl",
		RequestTimeout:          "5s",
		WaitTimeout:             "15m",
		Bazel:                   "bazelisk",
		Target:                  "//src/products/company/site:push-harbor",
		Namespace:               "tenant-root",
		Secret:                  "harbor-guardian-credentials",
		Host:                    "harbor.guardianintelligence.org",
		Project:                 "guardian",
		ProjectPublic:           true,
		PortForwardService:      "harbor-guardian",
		PortForwardReadyTimeout: "10s",
		Workspace:               ".",
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

	badProject := cfg
	badProject.Project = "Guardian"
	if err := validateConfig(badProject); err == nil {
		t.Fatalf("invalid project accepted")
	}

	badPortForwardReadyTimeout := cfg
	badPortForwardReadyTimeout.PortForwardReadyTimeout = "eventually"
	if err := validateConfig(badPortForwardReadyTimeout); err == nil {
		t.Fatalf("invalid port-forward ready timeout accepted")
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

func TestEnsureHarborProjectExists(t *testing.T) {
	var putSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireBasicAuth(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects/guardian":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2.0/projects/guardian":
			putSeen = true
			var payload harborProjectRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode update payload: %v", err)
			}
			if payload.Metadata["public"] != "true" {
				t.Fatalf("update payload = %#v", payload)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := publishConfig{Host: server.URL, Project: "guardian", ProjectPublic: true}
	if err := ensureHarborProject(t.Context(), cfg, "secret"); err != nil {
		t.Fatalf("ensureHarborProject() error = %v", err)
	}
	if !putSeen {
		t.Fatalf("project visibility update was not called")
	}
}

func TestEnsureHarborProjectCreatesMissingProject(t *testing.T) {
	var postSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireBasicAuth(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects/guardian":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/projects":
			postSeen = true
			var payload harborProjectRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			if payload.ProjectName != "guardian" || !payload.Public || payload.Metadata["public"] != "true" {
				t.Fatalf("create payload = %#v", payload)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := publishConfig{Host: server.URL, Project: "guardian", ProjectPublic: true}
	if err := ensureHarborProject(t.Context(), cfg, "secret"); err != nil {
		t.Fatalf("ensureHarborProject() error = %v", err)
	}
	if !postSeen {
		t.Fatalf("project create was not called")
	}
}

func TestEnsureHarborProjectRejectsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("nope"))
	}))
	defer server.Close()

	cfg := publishConfig{Host: server.URL, Project: "guardian", ProjectPublic: true}
	if err := ensureHarborProject(t.Context(), cfg, "secret"); err == nil {
		t.Fatalf("unauthorized response was accepted")
	}
}

func TestHarborAPIURL(t *testing.T) {
	got, err := harborAPIURL("harbor.guardianintelligence.org", "/api/v2.0/projects")
	if err != nil {
		t.Fatalf("harborAPIURL() error = %v", err)
	}
	if got != "https://harbor.guardianintelligence.org/api/v2.0/projects" {
		t.Fatalf("harborAPIURL() = %q", got)
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

func requireBasicAuth(t *testing.T, r *http.Request) {
	t.Helper()
	user, password, ok := r.BasicAuth()
	if !ok || user != "admin" || password != "secret" {
		t.Fatalf("missing or invalid basic auth")
	}
}
