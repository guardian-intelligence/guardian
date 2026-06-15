package main

import "testing"

// TestComponentsTable pins the structural invariants of the components
// table that nothing in the type system enforces:
//
//   - a manifest-only component (no image layouts) MUST be deliberately
//     gated (enabled != nil): with no image and no site gate there would be
//     nothing deliberate about where its objects land;
//   - OpenBao, Crossplane, and ESO apply before pods that consume projected
//     Secrets;
//   - the Crossplane substrate applies before Guardian platform APIs, and
//     product route owners apply after the platform substrate.
func TestComponentsTable(t *testing.T) {
	indexOf := make(map[string]int, len(components))
	for i, c := range components {
		if c.name == "" {
			t.Errorf("components[%d]: name is required", i)
		}
		if c.manifest == "" && !c.pushOnly {
			t.Errorf("components[%d]: manifest is required unless pushOnly (got name=%q)", i, c.name)
		}
		if c.pushOnly && len(c.imageLayouts()) == 0 {
			t.Errorf("component %q is pushOnly but has no image layout", c.name)
		}
		if _, dup := indexOf[c.name]; dup {
			t.Errorf("components: duplicate name %q", c.name)
		}
		indexOf[c.name] = i
		if len(c.imageLayouts()) == 0 && c.enabled == nil {
			t.Errorf("component %q is manifest-only (no image layouts) but ungated: manifest-only components must set enabled", c.name)
		}
	}
	for _, rel := range []struct {
		before string
		after  string
		why    string
	}{
		{"crossplane", "provider-kubernetes", "Crossplane package CRDs and controllers must exist before provider packages"},
		{"cert-manager", "edge-gateway-platform", "platform TLS certificates require cert-manager CRDs"},
		{"provider-kubernetes", "provider-kubernetes-config", "ProviderConfig requires provider-kubernetes CRDs"},
		{"provider-kubernetes-config", "edge-gateway-platform", "the EdgeGateway composition emits provider-kubernetes Objects"},
		{"provider-kubernetes-config", "secret-projection-platform", "the SecretProjection composition emits provider-kubernetes Objects"},
		{"provider-kubernetes-config", "public-http-service-platform", "the PublicHttpService composition emits provider-kubernetes Objects"},
		{"provider-kubernetes-config", "directus-platform", "the DirectusInstance composition emits provider-kubernetes Objects"},
		{"public-http-service-platform", "aisucks-product-api", "product APIs compose PublicHttpService"},
		{"public-http-service-platform", "company-site-product-api", "product APIs compose PublicHttpService"},
		{"aisucks-product-api", "aisucks", "product images are consumed by product XRs"},
		{"company-site-product-api", "company-site", "product images are consumed by product XRs"},
		{"edge-gateway-platform", "status", "product routes attach to the platform Gateway listener"},
		{"edge-gateway-platform", "zot", "product routes attach to the platform Gateway listener"},
		{"openbao", "external-secrets", "ESO authenticates to Bao"},
		{"external-secrets", "clickhouse", "ClickHouse requires clickhouse-admin at pod config time"},
		{"external-secrets", "grafana", "Grafana requires grafana-admin at pod config time"},
		{"external-secrets", "zot", "zot requires the OpenBao-projected htpasswd file at pod config time"},
	} {
		bi, ok := indexOf[rel.before]
		if !ok {
			t.Fatalf("components: %s entry missing", rel.before)
		}
		ai, ok := indexOf[rel.after]
		if !ok {
			t.Fatalf("components: %s entry missing", rel.after)
		}
		if bi > ai {
			t.Errorf("components: %s (index %d) must apply before %s (index %d): %s", rel.before, bi, rel.after, ai, rel.why)
		}
	}
}
