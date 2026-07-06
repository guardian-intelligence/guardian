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

func testImportEnv() map[string]string {
	return map[string]string{
		"cloudflare_account_id":                                   "account",
		"cloudflare_r2_api_token":                                 "r2-api",
		"cloudflare_r2_secret_access_key":                         "r2-secret",
		"cloudflare_r2_s3_api_endpoint":                           "r2-endpoint",
		"cloudflare_r2_access_key_id":                             "r2-access",
		"cloudflare_r2_backups_access_key_id":                     "backups-access",
		"cloudflare_r2_backups_secret_access_key":                 "backups-secret",
		"cloudflare_guardian_intelligence_org_dnz_zone_api_token": "zone",
		"cloudflare_external_dns_api_token":                       "external",
		"cloudflare_dns_lb_provisioner_api_token":                 "lb",
		"guardian_alerting_ntfy_url":                              "https://ntfy.sh/guardian-topic",
		"platform_admin_shovon_password":                          "shovon-pass",
		"platform_admin_guardian_ops_password":                    "guardian-ops-pass",
		"github_promotions_app_private_key_b64":                   base64.StdEncoding.EncodeToString([]byte(testGithubAppPEM)),
		"github_runner_app_prod_app_id":                           "3370540",
		"github_runner_app_prod_client_id":                        "Iv23xxxx",
		"github_runner_app_prod_webhook_secret":                   "runner-webhook",
		"github_runner_app_prod_client_secret":                    "runner-client",
		"github_runner_app_prod_private_key_b64":                  base64.StdEncoding.EncodeToString([]byte(testRunnerAppPEM)),
	}
}

func TestImportPlan(t *testing.T) {
	plan, err := importPlan(testImportEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 9 {
		t.Fatalf("plan length = %d, want 9", len(plan))
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
	backups := plan[3]
	if backups.APIPath != "kv/data/guardian/guardian-mgmt/tenant-root/backups-r2" {
		t.Fatalf("backups path = %q", backups.APIPath)
	}
	// Key names are the backupstrategy-controller's flat-key credentials
	// contract; the tenant-root ExternalSecret maps them 1:1.
	if backups.Data["accessKey"] != "backups-access" || backups.Data["secretKey"] != "backups-secret" {
		t.Fatalf("backups keypair = %#v", backups.Data)
	}
	if backups.Data["endpoint"] != "r2-endpoint" || backups.Data["bucketName"] != "guardian-backups" {
		t.Fatalf("backups coordinates = %#v", backups.Data)
	}
	if backups.Data["region"] != "auto" {
		t.Fatalf("backups region = %q, want auto (clickhouse-backup sidecar reads it via secretKeyRef)", backups.Data["region"])
	}
	alerting := plan[4]
	if alerting.APIPath != "kv/data/guardian/guardian-mgmt/tenant-root/alerting" {
		t.Fatalf("alerting path = %q", alerting.APIPath)
	}
	// Key name is the alert-relay-config ExternalSecret's remoteRef property
	// (deployments/alerting/secrets.yaml maps it 1:1).
	if alerting.Data["ntfy_url"] != "https://ntfy.sh/guardian-topic" {
		t.Fatalf("alerting data = %#v", alerting.Data)
	}
	admins := plan[5]
	if admins.APIPath != "kv/data/guardian/guardian-mgmt/tenant-root/platform-admins" {
		t.Fatalf("platform-admins path = %q", admins.APIPath)
	}
	// Key names are the platform-admin-passwords ExternalSecret's remoteRef
	// properties (base/cozystack/platform-admins.yaml maps them 1:1 to the
	// KeycloakRealmUser passwordSecret keys).
	if admins.Data["shovon"] != "shovon-pass" || admins.Data["guardian-ops"] != "guardian-ops-pass" {
		t.Fatalf("platform-admins data = %#v", admins.Data)
	}
	promotion := plan[6]
	if promotion.APIPath != "kv/data/guardian/guardian-mgmt/company-site/promotion/github-app" {
		t.Fatalf("promotion path = %q", promotion.APIPath)
	}
	if promotion.Data["githubAppPrivateKey"] != testGithubAppPEM {
		t.Fatal("githubAppPrivateKey did not round-trip through base64")
	}
	runner := plan[8]
	if runner.APIPath != "kv/data/guardian/guardian-mgmt/verself-runner/github-app" {
		t.Fatalf("verself-runner path = %q", runner.APIPath)
	}
	if runner.Data["webhookSecret"] != "runner-webhook" || runner.Data["clientSecret"] != "runner-client" {
		t.Fatalf("verself-runner secrets = %#v", runner.Data)
	}
	if runner.Data["appId"] != "3370540" || runner.Data["clientId"] != "Iv23xxxx" {
		t.Fatalf("verself-runner identity = %#v", runner.Data)
	}
	if runner.Data["githubAppPrivateKey"] != testRunnerAppPEM {
		t.Fatal("verself-runner githubAppPrivateKey did not round-trip through base64")
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
	if len(plan) != 11 {
		t.Fatalf("plan length = %d, want 11 (9 base + beta + prod)", len(plan))
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
