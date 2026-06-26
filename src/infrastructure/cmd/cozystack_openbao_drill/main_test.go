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

	apiState := cfg
	apiState.Mode = "api-state"
	if err := validateConfig(apiState); err != nil {
		t.Fatalf("api-state mode rejected: %v", err)
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

func TestOpenBaoAPIStateAssertions(t *testing.T) {
	mounts := `{
		"kv/": {"type": "kv", "options": {"version": "2"}},
		"transit/": {"type": "transit", "options": null},
		"kubernetes/": {"type": "kubernetes", "options": null}
	}`
	if err := assertMount(mounts, "kv/", "kv", map[string]string{"version": "2"}); err != nil {
		t.Fatalf("kv mount rejected: %v", err)
	}
	if err := assertMount(mounts, "transit/", "transit", nil); err != nil {
		t.Fatalf("transit mount rejected: %v", err)
	}
	if err := assertMount(mounts, "missing/", "kv", nil); err == nil {
		t.Fatalf("missing mount accepted")
	}

	key := `{"data":{"name":"guardian-integrations-encryption","type":"aes256-gcm96","deletion_allowed":false,"exportable":false}}`
	if err := assertTransitKey(key, "guardian-integrations-encryption", "aes256-gcm96"); err != nil {
		t.Fatalf("transit key rejected: %v", err)
	}

	policy := `path "kv/data/guardian/guardian-mgmt/integrations/*" {
  capabilities = ["read"]
}`
	if err := assertPolicyContains(policy, "guardian-third-party-secret-reader", []string{`path "kv/data/guardian/guardian-mgmt/integrations/*"`, `capabilities = ["read"]`}); err != nil {
		t.Fatalf("policy rejected: %v", err)
	}

	role := `{"data":{
		"bound_service_account_names":["github-actions-runner-controller","github-app-secrets"],
		"bound_service_account_namespaces":"arc-systems,tenant-guardian-release,tenant-guardian-secrets-prod",
		"token_policies":["guardian-third-party-secret-reader","guardian-third-party-transit-client"],
		"audience":"openbao"
	}}`
	if err := assertKubernetesAuthRole(
		role,
		"guardian-github-integrations",
		[]string{"github-actions-runner-controller", "github-app-secrets"},
		[]string{"arc-systems", "tenant-guardian-release", "tenant-guardian-secrets-prod"},
		[]string{"guardian-third-party-secret-reader", "guardian-third-party-transit-client"},
		"openbao",
	); err != nil {
		t.Fatalf("github role rejected: %v", err)
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
