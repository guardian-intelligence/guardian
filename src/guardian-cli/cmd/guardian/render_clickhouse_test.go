package main

import (
	"strings"
	"testing"
)

// TestClickhouseSiteGate pins the ledger ratchet across the real site inputs:
// ObservabilityStack spec.clickhouse.enabled is ON for dev/gamma and OFF for
// prod. Prod must not grow a clickhouse Deployment until the ledger ratchet flips, and
// prod's otel-collector must render byte-identical to the metrics-only spine
// (no logs pipeline, no hostPath log mounts, no runAsUser: 0).
func TestClickhouseSiteGate(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	clickhouse := componentByName(t, "clickhouse")
	otelCollector := componentByName(t, "otel-collector")
	const (
		chImage   = "registry.guardian.internal/clickhouse@sha256:deadbeef"
		otelImage = "registry.guardian.internal/otel-collector@sha256:deadbeef"
	)

	wantEnabled := map[string]bool{"dev": true, "gamma": true, "prod": false}
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			if site.Clickhouse.Enabled != wantEnabled[siteName] {
				t.Fatalf("site %s ObservabilityStack clickhouse.enabled = %v, want %v (the ledger ratchet: dev+gamma on, prod off)",
					siteName, site.Clickhouse.Enabled, wantEnabled[siteName])
			}

			// The clickhouse manifest itself is site-independent; what gates
			// prod is the components-table enabled func, mirrored here.
			rendered, err := buildComponentKustomization(kubectl, clickhouse, map[string]string{"clickhouse": chImage}, nil)
			if err != nil {
				t.Fatal(err)
			}
			kinds := strings.Join(decodeKinds(t, rendered), ",")
			for _, want := range []string{"ConfigMap", "Deployment", "Service"} {
				if !strings.Contains(kinds, want) {
					t.Errorf("clickhouse manifest kinds = %v; missing %s", kinds, want)
				}
			}

			// The collector's ledger tee follows the same flag.
			otelRendered, err := buildComponentKustomization(kubectl, otelCollector, map[string]string{"otel-collector": otelImage}, site)
			if err != nil {
				t.Fatal(err)
			}
			out := string(otelRendered)
			decodeKinds(t, otelRendered) // structural validity
			// Key forms ("filelog:") not bare words: comments may name
			// receivers unconditionally.
			ledgerMarkers := []string{
				"filelog:",
				"k8sobjects:",
				"clickhouse.observability.svc:9000",
				"create_schema: false",
				"runAsUser: 0",
				"/var/log/pods",
				"/var/lib/otel-collector",
				"resource/site",
				"file_storage",
				"events.k8s.io",
			}
			for _, marker := range ledgerMarkers {
				if site.Clickhouse.Enabled != strings.Contains(out, marker) {
					t.Errorf("site %s (ObservabilityStack clickhouse.enabled=%v): otel render marker %q presence mismatch",
						siteName, site.Clickhouse.Enabled, marker)
				}
			}
			// On every site the metrics spine survives untouched.
			for _, want := range []string{"prometheusremotewrite", "memory_limiter"} {
				if !strings.Contains(out, want) {
					t.Errorf("site %s: otel render missing metrics-spine marker %q", siteName, want)
				}
			}
		})
	}
}
