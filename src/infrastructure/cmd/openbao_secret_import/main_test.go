package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
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

const testOriginCertificatePEM = "-----BEGIN CERTIFICATE-----\nnot-a-real-certificate\n-----END CERTIFICATE-----\n"

const testOriginPrivateKeyPEM = "-----BEGIN EC PRIVATE KEY-----\nnot-a-real-origin-key\n-----END EC PRIVATE KEY-----\n"

func testImportEnv() map[string]string {
	return map[string]string{
		"cloudflare_account_id":                  "account",
		"cloudflare_r2_secret_access_key":        "r2-secret",
		"cloudflare_r2_s3_api_endpoint":          "r2-endpoint",
		"cloudflare_r2_access_key_id":            "r2-access",
		"cloudflare_origin_certificate_b64":      base64.StdEncoding.EncodeToString([]byte(testOriginCertificatePEM)),
		"cloudflare_origin_private_key_b64":      base64.StdEncoding.EncodeToString([]byte(testOriginPrivateKeyPEM)),
		"guardian_alerting_ntfy_url":             "https://ntfy.sh/guardian-topic",
		"platform_admin_password":                "admin-pass",
		"platform_agent_password":                "agent-pass",
		"github_promotions_app_private_key_b64":  base64.StdEncoding.EncodeToString([]byte(testGithubAppPEM)),
		"github_runner_app_prod_app_id":          "3370540",
		"github_runner_app_prod_client_id":       "Iv23xxxx",
		"github_runner_app_prod_webhook_secret":  "runner-webhook",
		"github_runner_app_prod_client_secret":   "runner-client",
		"github_runner_app_prod_private_key_b64": base64.StdEncoding.EncodeToString([]byte(testRunnerAppPEM)),
		"zot_countersigner_password":             "zot-push-pass",
		"github_projector_pat":                   "ghp-projector-pat",
	}
}

func TestImportPlan(t *testing.T) {
	plan, err := importPlan(testImportEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 10 {
		t.Fatalf("plan length = %d, want 10", len(plan))
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
	// properties (base/cozystack/platform-admins.yaml maps them 1:1 to the
	// KeycloakRealmUser passwordSecret keys).
	if admins.Data["platform-admin"] != "admin-pass" || admins.Data["platform-agent"] != "agent-pass" {
		t.Fatalf("platform-admins data = %#v", admins.Data)
	}
	promotion, ok := byPath["kv/data/guardian/guardian-mgmt/company-site/promotion/github-app"]
	if !ok {
		t.Fatal("company-site promotion write missing")
	}
	if promotion.Data["githubAppPrivateKey"] != testGithubAppPEM {
		t.Fatal("githubAppPrivateKey did not round-trip through base64")
	}
	productsPromotion, ok := byPath["kv/data/guardian/guardian-mgmt/guardian-products/promotion/github-app"]
	if !ok {
		t.Fatal("products promotion write missing")
	}
	if productsPromotion.Data["githubAppPrivateKey"] != testGithubAppPEM {
		t.Fatal("products githubAppPrivateKey did not round-trip through base64")
	}
	runner, ok := byPath["kv/data/guardian/guardian-mgmt/postflight-runner/github-app"]
	if !ok {
		t.Fatal("postflight-runner write missing")
	}
	if runner.Data["webhookSecret"] != "runner-webhook" || runner.Data["clientSecret"] != "runner-client" {
		t.Fatalf("postflight-runner secrets = %#v", runner.Data)
	}
	if runner.Data["appId"] != "3370540" || runner.Data["clientId"] != "Iv23xxxx" {
		t.Fatalf("postflight-runner identity = %#v", runner.Data)
	}
	if runner.Data["githubAppPrivateKey"] != testRunnerAppPEM {
		t.Fatal("postflight-runner githubAppPrivateKey did not round-trip through base64")
	}
	zot, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian/zot-countersigner"]
	if !ok {
		t.Fatal("zot-countersigner write missing")
	}
	if zot.Data["password"] != "zot-push-pass" {
		t.Fatalf("zot-countersigner password = %q", zot.Data["password"])
	}
	// The htpasswd line is what zot's auth file mounts; the hash re-salts per
	// import, so verify it against the password instead of a fixed string.
	user, hash, found := strings.Cut(zot.Data["htpasswd"], ":")
	if !found || user != "countersigner" {
		t.Fatalf("zot-countersigner htpasswd line = %q, want countersigner:<bcrypt>", zot.Data["htpasswd"])
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("zot-push-pass")); err != nil {
		t.Fatalf("zot-countersigner htpasswd hash does not verify against the password: %v", err)
	}
	projector, ok := byPath["kv/data/guardian/guardian-mgmt/tenant-guardian/github-projector"]
	if !ok {
		t.Fatal("github-projector write missing")
	}
	if projector.Data["token"] != "ghp-projector-pat" {
		t.Fatalf("github-projector token = %q", projector.Data["token"])
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
	if len(plan) != 12 {
		t.Fatalf("plan length = %d, want 12 (10 base + beta + prod)", len(plan))
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
