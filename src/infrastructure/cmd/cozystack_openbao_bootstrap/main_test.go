package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	cfg := bootstrapConfig{
		Kubectl:              "/kubectl",
		Tofu:                 "/tofu",
		Namespace:            "tenant-guardian",
		StatefulSet:          "guardian-openbao",
		Service:              "guardian-openbao-active",
		Root:                 "/repo/src/infrastructure/bootstrap/openbao-root-bootstrap",
		BackendEndpoint:      "https://account.r2.cloudflarestorage.com",
		Mode:                 "apply",
		AuthPath:             "kubernetes",
		OpsServiceAccount:    "openbao-ops-controller",
		OpsRole:              "guardian-openbao-ops-controller",
		TokenAudience:        "openbao",
		TokenDuration:        "10m",
		PortForwardReadyWait: time.Second,
		LoginVerifyTimeout:   time.Second,
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

	badRole := cfg
	badRole.OpsRole = "guardian openbao"
	if err := validateConfig(badRole); err == nil {
		t.Fatalf("invalid role accepted")
	}

	badTokenDuration := cfg
	badTokenDuration.TokenDuration = "later"
	if err := validateConfig(badTokenDuration); err == nil {
		t.Fatalf("invalid token duration accepted")
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
	root := "/repo/src/infrastructure/bootstrap/openbao-root-bootstrap"
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

func TestRequiredReadyReplicas(t *testing.T) {
	for _, tc := range []struct {
		replicas int
		want     int
	}{
		{replicas: 1, want: 1},
		{replicas: 2, want: 2},
		{replicas: 3, want: 2},
		{replicas: 5, want: 3},
	} {
		if got := requiredReadyReplicas(tc.replicas); got != tc.want {
			t.Fatalf("requiredReadyReplicas(%d) = %d, want %d", tc.replicas, got, tc.want)
		}
	}
}

func TestParseStatefulSetReplicaStatus(t *testing.T) {
	got, err := parseStatefulSetReplicaStatus(`{"spec":{"replicas":3},"status":{"readyReplicas":2}}`)
	if err != nil {
		t.Fatalf("parseStatefulSetReplicaStatus() error = %v", err)
	}
	if got.Replicas != 3 || got.ReadyReplicas != 2 {
		t.Fatalf("parseStatefulSetReplicaStatus() = %#v, want replicas=3 readyReplicas=2", got)
	}

	defaulted, err := parseStatefulSetReplicaStatus(`{"spec":{},"status":{}}`)
	if err != nil {
		t.Fatalf("parseStatefulSetReplicaStatus(defaulted) error = %v", err)
	}
	if defaulted.Replicas != 1 || defaulted.ReadyReplicas != 0 {
		t.Fatalf("parseStatefulSetReplicaStatus(defaulted) = %#v, want replicas=1 readyReplicas=0", defaulted)
	}
}

func TestParseStatefulSetReplicaStatusRejectsZeroReplicas(t *testing.T) {
	if _, err := parseStatefulSetReplicaStatus(`{"spec":{"replicas":0},"status":{}}`); err == nil {
		t.Fatalf("parseStatefulSetReplicaStatus() accepted zero replicas")
	}
}

func TestParseReadyEndpointSliceAddresses(t *testing.T) {
	const raw = `{
		"items": [
			{
				"endpoints": [
					{"addresses": ["10.244.9.15"], "conditions": {"ready": true}},
					{"addresses": ["10.244.9.16"], "conditions": {"ready": false}},
					{"addresses": ["10.244.9.17"], "conditions": {}}
				]
			}
		]
	}`

	got, err := parseReadyEndpointSliceAddresses(raw)
	if err != nil {
		t.Fatalf("parseReadyEndpointSliceAddresses() error = %v", err)
	}
	if got != 2 {
		t.Fatalf("parseReadyEndpointSliceAddresses() = %d, want 2", got)
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
		"svc/guardian-openbao-active",
		"18200:8200",
	}
	if got := openBaoPortForwardArgs(runner, "guardian-openbao-active", 18200); !reflect.DeepEqual(got, want) {
		t.Fatalf("openBaoPortForwardArgs() = %#v, want %#v", got, want)
	}
}

func TestServiceAccountTokenArgs(t *testing.T) {
	want := []string{
		"create",
		"token",
		"openbao-ops-controller",
		"--audience=openbao",
		"--duration=10m",
	}
	if got := serviceAccountTokenArgs("openbao-ops-controller", "openbao", "10m"); !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceAccountTokenArgs() = %#v, want %#v", got, want)
	}
}

func TestOpenBaoKubernetesLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/auth/kubernetes/login" {
			t.Fatalf("path = %s, want /v1/auth/kubernetes/login", r.URL.Path)
		}
		var got map[string]string
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got["role"] != "guardian-openbao-ops-controller" || got["jwt"] != "jwt-secret" {
			t.Fatalf("login payload = %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"auth":{"client_token":"client-token"}}`)
	}))
	defer server.Close()

	if err := openBaoKubernetesLogin(context.Background(), server.URL, "kubernetes", "guardian-openbao-ops-controller", "jwt-secret", time.Second); err != nil {
		t.Fatalf("openBaoKubernetesLogin() error = %v", err)
	}
}

func TestOpenBaoKubernetesLoginRedactsJWTOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid jwt-secret", http.StatusBadRequest)
	}))
	defer server.Close()

	err := openBaoKubernetesLogin(context.Background(), server.URL, "kubernetes", "guardian-openbao-ops-controller", "jwt-secret", time.Second)
	if err == nil {
		t.Fatalf("openBaoKubernetesLogin() accepted failed login")
	}
	if strings.Contains(err.Error(), "jwt-secret") {
		t.Fatalf("login error leaked jwt: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("login error did not include redaction marker: %v", err)
	}
}

func TestOpenBaoKubernetesLoginURL(t *testing.T) {
	got, err := openBaoKubernetesLoginURL("http://127.0.0.1:18200/", "/kubernetes/")
	if err != nil {
		t.Fatalf("openBaoKubernetesLoginURL() error = %v", err)
	}
	if got != "http://127.0.0.1:18200/v1/auth/kubernetes/login" {
		t.Fatalf("openBaoKubernetesLoginURL() = %q", got)
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
