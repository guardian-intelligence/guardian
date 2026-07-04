package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	got, err := parseEnv([]byte(`
# comment
export cloudflare_account_id = "account"
cloudflare_external_dns_api_token='external'
cloudflare_r2_api_token=r2-token
`))
	if err != nil {
		t.Fatal(err)
	}
	if got["cloudflare_account_id"] != "account" {
		t.Fatalf("cloudflare_account_id = %q", got["cloudflare_account_id"])
	}
	if got["cloudflare_external_dns_api_token"] != "external" {
		t.Fatalf("cloudflare_external_dns_api_token = %q", got["cloudflare_external_dns_api_token"])
	}
	if got["cloudflare_r2_api_token"] != "r2-token" {
		t.Fatalf("cloudflare_r2_api_token = %q", got["cloudflare_r2_api_token"])
	}
}

func TestParseEnvRejectsMalformedLine(t *testing.T) {
	_, err := parseEnv([]byte("not valid\n"))
	if err == nil {
		t.Fatal("parseEnv accepted malformed line")
	}
}

const testGithubAppPEM = "-----BEGIN PRIVATE KEY-----\nnot-a-real-key\n-----END PRIVATE KEY-----\n"

func testImportEnv() map[string]string {
	return map[string]string{
		"cloudflare_account_id":                                   "account",
		"cloudflare_r2_api_token":                                 "r2-api",
		"cloudflare_r2_secret_access_key":                         "r2-secret",
		"cloudflare_r2_s3_api_endpoint":                           "r2-endpoint",
		"cloudflare_r2_access_key_id":                             "r2-access",
		"cloudflare_guardian_intelligence_org_dnz_zone_api_token": "zone",
		"cloudflare_external_dns_api_token":                       "external",
		"cloudflare_dns_lb_provisioner_api_token":                 "lb",
		"github_promotions_app_private_key_b64":                   base64.StdEncoding.EncodeToString([]byte(testGithubAppPEM)),
	}
}

func TestImportPlan(t *testing.T) {
	plan, err := importPlan(testImportEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 4 {
		t.Fatalf("plan length = %d, want 4", len(plan))
	}
	external := plan[0]
	if external.APIPath != "kv/data/guardian/guardian-mgmt/external-dns/cloudflare" {
		t.Fatalf("external path = %q", external.APIPath)
	}
	if external.Data["CF_API_TOKEN"] != "external" {
		t.Fatalf("CF_API_TOKEN = %q", external.Data["CF_API_TOKEN"])
	}
	paths := []string{plan[1].APIPath, plan[2].APIPath}
	if !strings.Contains(strings.Join(paths, "\n"), "operator/cloudflare") {
		t.Fatalf("operator cloudflare path missing from %#v", paths)
	}
	if !strings.Contains(strings.Join(paths, "\n"), "operator/r2") {
		t.Fatalf("operator r2 path missing from %#v", paths)
	}
	promotion := plan[3]
	if promotion.APIPath != "kv/data/guardian/guardian-mgmt/company-site/promotion/github-app" {
		t.Fatalf("promotion path = %q", promotion.APIPath)
	}
	if promotion.Data["githubAppPrivateKey"] != testGithubAppPEM {
		t.Fatal("githubAppPrivateKey did not round-trip through base64")
	}
}

func TestImportPlanRejectsBadGithubKey(t *testing.T) {
	env := testImportEnv()
	env["github_promotions_app_private_key_b64"] = "%%% not base64 %%%"
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted invalid base64")
	}
	env["github_promotions_app_private_key_b64"] = base64.StdEncoding.EncodeToString([]byte("plain text, not a PEM"))
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted a non-PEM payload")
	}
}

func TestImportPlanOptionalKeycloakStages(t *testing.T) {
	env := testImportEnv()
	env["BETA_GITHUB_CLIENT_SECRET"] = "beta-secret"
	env["PROD_GITHUB_CLIENT_SECRET"] = "prod-secret"
	// gamma deliberately absent: an env file may carry only a subset of stages.

	plan, err := importPlan(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 6 {
		t.Fatalf("plan length = %d, want 6 (4 base + beta + prod)", len(plan))
	}
	byPath := map[string]secretWrite{}
	for _, w := range plan {
		byPath[w.APIPath] = w
	}
	beta, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-beta/keycloak/github-oauth"]
	if !ok {
		t.Fatal("beta keycloak write missing")
	}
	if beta.Data["GITHUB_CLIENT_SECRET"] != "beta-secret" {
		t.Fatalf("beta GITHUB_CLIENT_SECRET = %q", beta.Data["GITHUB_CLIENT_SECRET"])
	}
	prod, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-prod/keycloak/github-oauth"]
	if !ok {
		t.Fatal("prod keycloak write missing")
	}
	if prod.Data["GITHUB_CLIENT_SECRET"] != "prod-secret" {
		t.Fatalf("prod GITHUB_CLIENT_SECRET = %q", prod.Data["GITHUB_CLIENT_SECRET"])
	}
	if _, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-gamma/keycloak/github-oauth"]; ok {
		t.Fatal("gamma keycloak write present despite no gamma secret in env")
	}
}

func TestImportPlanKeycloakGeneratedCredentials(t *testing.T) {
	env := testImportEnv()
	env["BETA_KEYCLOAK_ADMIN_BOOTSTRAP_USERNAME"] = "guardian-admin"
	env["BETA_KEYCLOAK_ADMIN_BOOTSTRAP_PASSWORD"] = "admin-pass"
	env["BETA_KEYCLOAK_CANARY_USER_USERNAME"] = "canary"
	env["BETA_KEYCLOAK_CANARY_USER_PASSWORD"] = "canary-pass"

	plan, err := importPlan(env)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]secretWrite{}
	for _, w := range plan {
		byPath[w.APIPath] = w
	}
	admin, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-beta/keycloak/admin-bootstrap"]
	if !ok {
		t.Fatal("beta admin-bootstrap write missing")
	}
	if admin.Data["username"] != "guardian-admin" || admin.Data["password"] != "admin-pass" {
		t.Fatalf("admin-bootstrap data = %#v", admin.Data)
	}
	canary, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-beta/keycloak/canary-user"]
	if !ok {
		t.Fatal("beta canary-user write missing")
	}
	if canary.Data["password"] != "canary-pass" {
		t.Fatalf("canary-user data = %#v", canary.Data)
	}

	// A username without its password (or vice versa) is a custody file bug,
	// not a partial import.
	env["GAMMA_KEYCLOAK_CANARY_USER_USERNAME"] = "canary"
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted a username without its password")
	}
}

func TestImportPlanMissingRequired(t *testing.T) {
	_, err := importPlan(map[string]string{})
	if err == nil {
		t.Fatal("importPlan accepted empty env")
	}
	if !strings.Contains(err.Error(), "cloudflare_account_id") {
		t.Fatalf("missing error did not name cloudflare_account_id: %v", err)
	}
}

func TestKubectlArgs(t *testing.T) {
	runner := kubectlRunner{
		kubeconfig:     "/tmp/kubeconfig",
		kubeAPIServer:  "https://10.8.0.250:6443",
		requestTimeout: "15s",
		namespace:      "tenant-guardian",
	}
	got := runner.args("get", "pods")
	want := []string{
		"--kubeconfig", "/tmp/kubeconfig",
		"--server", "https://10.8.0.250:6443",
		"--request-timeout=15s",
		"-n", "tenant-guardian",
		"get", "pods",
	}
	if len(got) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
}
