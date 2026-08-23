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

const testRunnerAppPEM = "-----BEGIN PRIVATE KEY-----\nnot-a-real-runner-key\n-----END PRIVATE KEY-----\n"

const testCanaryLoopAppPEM = "-----BEGIN PRIVATE KEY-----\nnot-a-real-canary-loop-key\n-----END PRIVATE KEY-----\n"

const testOriginCertificatePEM = "-----BEGIN CERTIFICATE-----\nnot-a-real-certificate\n-----END CERTIFICATE-----\n"

const testOriginPrivateKeyPEM = "-----BEGIN EC PRIVATE KEY-----\nnot-a-real-origin-key\n-----END EC PRIVATE KEY-----\n"

func testImportEnv() map[string]string {
	return map[string]string{
		"cloudflare_account_id":                         "account",
		"cloudflare_r2_secret_access_key":               "r2-secret",
		"cloudflare_r2_s3_api_endpoint":                 "r2-endpoint",
		"cloudflare_r2_access_key_id":                   "r2-access",
		"cloudflare_origin_certificate_b64":             base64.StdEncoding.EncodeToString([]byte(testOriginCertificatePEM)),
		"cloudflare_origin_private_key_b64":             base64.StdEncoding.EncodeToString([]byte(testOriginPrivateKeyPEM)),
		"guardian_alerting_ntfy_url":                    "https://ntfy.sh/guardian-topic",
		"platform_admin_password":                       "admin-pass",
		"platform_agent_password":                       "agent-pass",
		"github_promotions_app_private_key_b64":         base64.StdEncoding.EncodeToString([]byte(testGithubAppPEM)),
		"github_runner_app_prod_app_id":                 "3370540",
		"github_runner_app_prod_webhook_secret":         "runner-webhook",
		"github_runner_app_prod_private_key_b64":        base64.StdEncoding.EncodeToString([]byte(testRunnerAppPEM)),
		"github_postflight_canary_loop_app_id":          "4382022",
		"github_postflight_canary_loop_installation_id": "148677496",
		"github_postflight_canary_loop_private_key_b64": base64.StdEncoding.EncodeToString([]byte(testCanaryLoopAppPEM)),
	}
}

func TestImportPlan(t *testing.T) {
	plan, err := importPlan(testImportEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 8 {
		t.Fatalf("plan length = %d, want 8", len(plan))
	}
	byPath := map[string]secretWrite{}
	for _, w := range plan {
		byPath[w.APIPath] = w
	}
	r2, ok := byPath["kv/data/guardian/guardian-mgmt/operator/r2"]
	if !ok {
		t.Fatal("operator r2 write missing")
	}
	if r2.Data["cloudflare_r2_access_key_id"] != "r2-access" || r2.Data["cloudflare_r2_secret_access_key"] != "r2-secret" {
		t.Fatalf("operator r2 keypair = %#v", r2.Data)
	}
	if r2.Data["cloudflare_r2_s3_api_endpoint"] != "r2-endpoint" {
		t.Fatalf("operator r2 endpoint = %q", r2.Data["cloudflare_r2_s3_api_endpoint"])
	}
	if _, ok := r2.Data["cloudflare_r2_api_token"]; ok {
		t.Fatalf("operator r2 carries cloudflare_r2_api_token: %#v", r2.Data)
	}
	originTLS, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-root/cloudflare-origin-tls"]
	if !ok {
		t.Fatal("Cloudflare origin TLS write missing")
	}
	if originTLS.Data["tls.crt"] != testOriginCertificatePEM || originTLS.Data["tls.key"] != testOriginPrivateKeyPEM {
		t.Fatal("Cloudflare origin TLS material did not round-trip through base64")
	}
	alerting, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-root/alerting"]
	if !ok {
		t.Fatal("alerting write missing")
	}
	// Key name is the alert-relay-config ExternalSecret's remoteRef property
	// (deployments/alerting/secrets.yaml maps it 1:1).
	if alerting.Data["ntfy_url"] != "https://ntfy.sh/guardian-topic" {
		t.Fatalf("alerting data = %#v", alerting.Data)
	}
	admins, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-root/platform-admins"]
	if !ok {
		t.Fatal("platform-admins write missing")
	}
	// Key names are the platform-admin-passwords ExternalSecret's remoteRef
	// properties (base/cozystack-identities/platform-admins.yaml maps them 1:1 to the
	// KeycloakRealmUser passwordSecret keys).
	if admins.Data["platform-admin"] != "admin-pass" || admins.Data["platform-agent"] != "agent-pass" {
		t.Fatalf("platform-admins data = %#v", admins.Data)
	}
	productsPromotion, ok := byPath["kv/data/guardian/guardian-mgmt/guardian-postflight/promotion/github-app"]
	if !ok {
		t.Fatal("products promotion write missing")
	}
	if productsPromotion.Data["githubAppPrivateKey"] != testGithubAppPEM {
		t.Fatal("products githubAppPrivateKey did not round-trip through base64")
	}
	imageopsPromotion, ok := byPath["kv/data/guardian/guardian-mgmt/guardian-imageops/promotion/github-app"]
	if !ok {
		t.Fatal("imageops promotion write missing")
	}
	if imageopsPromotion.Data["githubAppPrivateKey"] != testGithubAppPEM {
		t.Fatal("imageops githubAppPrivateKey did not round-trip through base64")
	}
	runner, ok := byPath["kv/data/guardian/guardian-mgmt/postflight-runner/github-app"]
	if !ok {
		t.Fatal("postflight-runner write missing")
	}
	if runner.Data["webhookSecret"] != "runner-webhook" {
		t.Fatalf("postflight-runner secrets = %#v", runner.Data)
	}
	if runner.Data["appId"] != "3370540" {
		t.Fatalf("postflight-runner identity = %#v", runner.Data)
	}
	if _, ok := runner.Data["clientSecret"]; ok {
		t.Fatalf("postflight-runner carries unused OAuth clientSecret: %#v", runner.Data)
	}
	if _, ok := runner.Data["clientId"]; ok {
		t.Fatalf("postflight-runner carries unused OAuth clientId: %#v", runner.Data)
	}
	if runner.Data["githubAppPrivateKey"] != testRunnerAppPEM {
		t.Fatal("postflight-runner githubAppPrivateKey did not round-trip through base64")
	}
	canaryLoop, ok := byPath["kv/data/guardian/guardian-mgmt/postflight-runner/canary-loop-github-app"]
	if !ok {
		t.Fatal("postflight canary-loop write missing")
	}
	if canaryLoop.Data["appId"] != "4382022" || canaryLoop.Data["installationId"] != "148677496" {
		t.Fatalf("postflight canary-loop identity = %#v", canaryLoop.Data)
	}
	if canaryLoop.Data["githubAppPrivateKey"] != testCanaryLoopAppPEM {
		t.Fatal("postflight canary-loop githubAppPrivateKey did not round-trip through base64")
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

	env = testImportEnv()
	env["github_runner_app_prod_private_key_b64"] = "%%% not base64 %%%"
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted invalid base64 for the runner app key")
	}
	env["github_runner_app_prod_private_key_b64"] = base64.StdEncoding.EncodeToString([]byte("plain text, not a PEM"))
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted a non-PEM payload for the runner app key")
	}

	env = testImportEnv()
	env["github_postflight_canary_loop_private_key_b64"] = "%%% not base64 %%%"
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted invalid base64 for the canary-loop app key")
	}
	env["github_postflight_canary_loop_private_key_b64"] = base64.StdEncoding.EncodeToString([]byte("plain text, not a PEM"))
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted a non-PEM payload for the canary-loop app key")
	}
}

func TestImportPlanOptionalKeycloakStages(t *testing.T) {
	env := testImportEnv()
	// PROD_GITHUB_CLIENT_SECRET deliberately absent: an env file may carry
	// only a subset of the optional Keycloak secrets.
	plan, err := importPlan(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 8 {
		t.Fatalf("plan length = %d, want 8 (base only)", len(plan))
	}

	env["PROD_GITHUB_CLIENT_SECRET"] = "prod-secret"
	plan, err = importPlan(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 9 {
		t.Fatalf("plan length = %d, want 9 (8 base + prod)", len(plan))
	}
	byPath := map[string]secretWrite{}
	for _, w := range plan {
		byPath[w.APIPath] = w
	}
	prod, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-prod/keycloak/github-oauth"]
	if !ok {
		t.Fatal("prod keycloak write missing")
	}
	if prod.Data["GITHUB_CLIENT_SECRET"] != "prod-secret" {
		t.Fatalf("prod GITHUB_CLIENT_SECRET = %q", prod.Data["GITHUB_CLIENT_SECRET"])
	}

	env["STAGING_GITHUB_CLIENT_SECRET"] = "staging-secret"
	plan, err = importPlan(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 10 {
		t.Fatalf("plan length = %d, want 10 (8 base + two environments)", len(plan))
	}
	byPath = map[string]secretWrite{}
	for _, w := range plan {
		byPath[w.APIPath] = w
	}
	staging, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-staging/keycloak/github-oauth"]
	if !ok {
		t.Fatal("staging keycloak write missing")
	}
	if staging.Data["GITHUB_CLIENT_SECRET"] != "staging-secret" {
		t.Fatalf("staging GITHUB_CLIENT_SECRET = %q", staging.Data["GITHUB_CLIENT_SECRET"])
	}
}

func TestImportPlanGitHubLoginCanary(t *testing.T) {
	env := testImportEnv()
	env["PROD_GITHUB_LOGIN_CANARY_USERNAME"] = "guardian-canary"
	env["PROD_GITHUB_LOGIN_CANARY_PASSWORD"] = "canary-pass"
	env["PROD_GITHUB_LOGIN_CANARY_TOTP_SECRET"] = "JBSWY3DPEHPK3PXP"

	plan, err := importPlan(env)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]secretWrite{}
	for _, w := range plan {
		byPath[w.APIPath] = w
	}
	canary, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-prod/keycloak/login-canary-github"]
	if !ok {
		t.Fatal("prod login-canary-github write missing")
	}
	if canary.Data["password"] != "canary-pass" || canary.Data["totp_secret"] != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("login-canary-github data = %#v", canary.Data)
	}

	delete(env, "PROD_GITHUB_LOGIN_CANARY_PASSWORD")
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted incomplete GitHub canary credentials")
	}
}

func TestImportPlanGitHubOrgCanary(t *testing.T) {
	env := testImportEnv()
	env["PROD_GITHUB_ORG_CANARY_USERNAME"] = "postflight-canary-001"
	env["PROD_GITHUB_ORG_CANARY_PASSWORD"] = "org-canary-pass"
	env["PROD_GITHUB_ORG_CANARY_TOTP_SECRET"] = "JBSWY3DPEHPK3PXP"

	plan, err := importPlan(env)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]secretWrite{}
	for _, w := range plan {
		byPath[w.APIPath] = w
	}
	canary, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian-prod/keycloak/org-canary-github"]
	if !ok {
		t.Fatal("prod org-canary-github write missing")
	}
	if canary.Data["username"] != "postflight-canary-001" || canary.Data["password"] != "org-canary-pass" {
		t.Fatalf("org-canary-github data = %#v", canary.Data)
	}

	delete(env, "PROD_GITHUB_ORG_CANARY_TOTP_SECRET")
	if _, err := importPlan(env); err == nil {
		t.Fatal("importPlan accepted incomplete org-canary credentials")
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
