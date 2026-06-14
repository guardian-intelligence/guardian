package main

import (
	"strings"
	"testing"
)

func TestEdgeGatewayPlatformRender(t *testing.T) {
	c := componentByName(t, "edge-gateway-platform")
	tmpl, err := toolPath("_main/" + c.manifest)
	if err != nil {
		t.Fatalf("locate edge gateway platform manifest: %v", err)
	}
	c.manifest = tmpl
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: edgegateways.platform.guardian.dev",
		"kind: Composition",
		"name: edge-gateway-cilium",
		"kind: GatewayClass",
		"kind: Gateway",
		"kind: ClusterIssuer",
		"kind: Certificate",
		"providerConfigRef:\n                name: edge-gateway",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("edge gateway platform render missing %q", want)
		}
	}
}

func TestEdgeGatewaySiteManifests(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + siteName + "/site.yaml")
			if err != nil {
				t.Fatalf("locate site.yaml: %v", err)
			}
			site, err := loadSite(sitePath)
			if err != nil {
				t.Fatal(err)
			}
			if !site.Gateway.Enabled {
				if len(site.Gateway.Manifests) != 0 {
					t.Fatalf("gateway manifests should be empty for %s, got %v", siteName, site.Gateway.Manifests)
				}
				return
			}
			if len(site.Gateway.Manifests) != 1 {
				t.Fatalf("gateway-enabled site should declare one EdgeGateway manifest, got %v", site.Gateway.Manifests)
			}
			raw, err := readSiteManifest(site.Gateway.Manifests[0])
			if err != nil {
				t.Fatal(err)
			}
			out := string(raw)
			for _, want := range []string{
				"kind: EdgeGateway",
				"name: tls-aisucks",
				"hostname: \"" + site.Aisucks.Domain + "\"",
				"name: https-oci",
				"hostname: \"" + site.OCI.Domain + "\"",
				"name: oci-guardianintelligence-org-tls",
				"dns01CloudflareSecretName: cloudflare-guardianintelligence-org-dns-token",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("edge gateway instance render missing %q", want)
				}
			}
			if strings.Contains(out, "HTTPRoute") || strings.Contains(out, "TLSRoute") {
				t.Error("EdgeGateway instance must not render product routes")
			}
		})
	}
}
