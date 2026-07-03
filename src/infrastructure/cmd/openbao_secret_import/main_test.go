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
	if external.APIPath != "kv/data/guardian/guardian-mgmt/tenant-guardian/dns/external-dns" {
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
