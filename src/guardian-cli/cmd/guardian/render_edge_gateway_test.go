package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestEdgeGatewayPlatformRender(t *testing.T) {
	out := buildTestPlatformPackage(t)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: edgegateways.platform.guardian.dev",
		"kind: Composition",
		"name: edge-gateway-cilium",
		"name: function-auto-ready",
		"kind: GatewayClass",
		"kind: Gateway",
		"kind: ClusterIssuer",
		"kind: Certificate",
		"providerConfigRef:",
		"name: edge-gateway",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("edge gateway platform render missing %q", want)
		}
	}
}

func TestEdgeGatewaySiteManifests(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + siteName + "/bootstrap.yaml")
			if err != nil {
				t.Fatalf("locate bootstrap.yaml: %v", err)
			}
			site, err := loadSite(sitePath)
			if err != nil {
				t.Fatal(err)
			}
			out := string(site.EnvironmentBundle.Raw)
			if !site.Gateway.Enabled {
				if strings.Contains(out, "kind: EdgeGateway") {
					t.Fatalf("gateway-disabled site should not declare an EdgeGateway XR")
				}
				return
			}
			if siteName == "gamma" && !strings.Contains(out, "privateKeyAlgorithm: ECDSA") {
				t.Error("gamma EdgeGateway should preserve restored ECDSA aisucks certificate")
			}
			for _, want := range []string{
				"kind: EdgeGateway",
				"name: https-aisucks",
				"hostname: \"" + site.Aisucks.Domain + "\"",
				"protocol: HTTPS",
				"mode: Terminate",
				"certificateRefName: aisucks-tls",
				"name: aisucks-tls",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("edge gateway instance render missing %q", want)
				}
			}
			if site.OCI.Domain != "" {
				for _, want := range []string{
					"name: https-oci",
					"hostname: \"" + site.OCI.Domain + "\"",
					"name: oci-guardianintelligence-org-tls",
					"dns01CloudflareSecretName: cloudflare-guardianintelligence-org-dns-token",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("edge gateway instance render missing %q", want)
					}
				}
			}
			if site.Company.Domain != "" {
				for _, want := range []string{
					"name: https-company-site",
					"hostname: \"" + site.Company.Domain + "\"",
					"protocol: HTTPS",
					"certificateRefName: company-site-tls",
					"name: company-site-tls",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("edge gateway instance render missing %q", want)
					}
				}
			}
			for i, domain := range site.Status.Domains {
				for _, want := range []string{
					"name: tls-status-" + strconv.Itoa(i),
					"hostname: \"" + domain + "\"",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("edge gateway instance render missing %q", want)
					}
				}
			}
			if strings.Contains(out, "HTTPRoute") || strings.Contains(out, "TLSRoute") {
				t.Error("EdgeGateway instance must not render product routes")
			}
		})
	}
}
