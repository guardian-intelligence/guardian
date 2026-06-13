package main

import "testing"

func TestLookupCloudflareDNSToken(t *testing.T) {
	values := map[string]string{
		"cloudflare_guardian_intelligence_org_dnz_zone_api_token": "legacy-token",
	}
	token, source := lookupCloudflareDNSToken(func(key string) string { return values[key] })
	if token != "legacy-token" {
		t.Fatalf("token = %q; want legacy-token", token)
	}
	if source != "cloudflare_guardian_intelligence_org_dnz_zone_api_token" {
		t.Fatalf("source = %q; want legacy env var", source)
	}

	values["CLOUDFLARE_GUARDIAN_INTELLIGENCE_ORG_DNS_ZONE_API_TOKEN"] = "preferred-token"
	token, source = lookupCloudflareDNSToken(func(key string) string { return values[key] })
	if token != "preferred-token" {
		t.Fatalf("token = %q; want preferred-token", token)
	}
	if source != "CLOUDFLARE_GUARDIAN_INTELLIGENCE_ORG_DNS_ZONE_API_TOKEN" {
		t.Fatalf("source = %q; want preferred env var", source)
	}
}
