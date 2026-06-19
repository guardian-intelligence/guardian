package aisucksv1

import (
	"testing"

	policyv1 "github.com/guardian-intelligence/guardian/src/products/aisucks/api/guardian/policy/v1"
)

func TestHealthOperationPolicy(t *testing.T) {
	policy, ok := OperationPolicy(AisucksServiceHealthProcedure)
	if !ok {
		t.Fatal("Health operation policy not found")
	}
	if policy.GetAuth() != policyv1.AuthRequirement_AUTH_REQUIREMENT_NONE {
		t.Fatalf("auth = %s; want %s", policy.GetAuth(), policyv1.AuthRequirement_AUTH_REQUIREMENT_NONE)
	}
	if policy.GetAuditLevel() != policyv1.AuditLevel_AUDIT_LEVEL_OPERATIONAL {
		t.Fatalf("audit_level = %s; want %s", policy.GetAuditLevel(), policyv1.AuditLevel_AUDIT_LEVEL_OPERATIONAL)
	}
	if policy.GetRiskTier() != policyv1.RiskTier_RISK_TIER_LOW {
		t.Fatalf("risk_tier = %s; want %s", policy.GetRiskTier(), policyv1.RiskTier_RISK_TIER_LOW)
	}
	if policy.GetMaxRequestBytes() != 1024 {
		t.Fatalf("max_request_bytes = %d; want 1024", policy.GetMaxRequestBytes())
	}
	if policy.GetRateLimit().GetRequests() != 60 || policy.GetRateLimit().GetWindow() != "1m" {
		t.Fatalf("rate_limit = %#v; want 60/1m", policy.GetRateLimit())
	}
	if policy.GetIdempotency() != policyv1.Idempotency_IDEMPOTENCY_IDEMPOTENT {
		t.Fatalf("idempotency = %s; want %s", policy.GetIdempotency(), policyv1.Idempotency_IDEMPOTENCY_IDEMPOTENT)
	}
}

func TestUnknownOperationPolicy(t *testing.T) {
	if policy, ok := OperationPolicy("/guardian.products.aisucks.v1.AisucksService/Missing"); ok || policy != nil {
		t.Fatalf("OperationPolicy returned %#v, %v; want nil, false", policy, ok)
	}
}
