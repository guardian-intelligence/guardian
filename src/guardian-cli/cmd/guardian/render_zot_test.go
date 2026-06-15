package main

import (
	"strings"
	"testing"
)

const zotTestImage = "registry.guardian.internal/zot@sha256:deadbeef"

func TestOCIRegistryPlatformRender(t *testing.T) {
	c := componentByName(t, "oci-registry-platform")
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: ociregistries.platform.guardian.dev",
		"kind: Composition",
		"name: oci-registry-kubernetes",
		"kind: ConfigMap",
		"kind: Deployment",
		"kind: Service",
		"kind: HTTPRoute",
		`"distSpecVersion": "1.1.1"`,
		`"search": {`,
		`"ui": {`,
		`"enable": true`,
		`"port": "5000"`,
		`"htpasswd": {`,
		`"path": "/zot-auth/htpasswd"`,
		`"anonymousPolicy": ["read"]`,
		`"users": ["guardian-release"]`,
		`"actions": ["read", "create", "update", "delete"]`,
		`image: {{ $spec.image }}`,
		`command: ["/usr/local/bin/zot-linux-amd64"]`,
		"containerPort: 5000",
		"secretName: {{ $spec.publisherSecret.name }}",
		"key: {{ $spec.publisherSecret.htpasswdKey }}",
		"hostPath:",
		"path: {{ $spec.storagePath }}",
		"sectionName: {{ $spec.gateway.httpsSectionName }}",
		"name: function-environment-configs",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("OCIRegistry platform render missing %q", want)
		}
	}
}

func TestOCIRegistryEnvironmentBundleInstances(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			rendered, err := renderEnvironmentBundle(site, testProductImages())
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			if siteName != "dev" {
				if strings.Contains(out, "kind: OCIRegistry") {
					t.Fatalf("OCIRegistry should not render for %s", siteName)
				}
				return
			}
			for _, want := range []string{
				"kind: OCIRegistry",
				"name: zot",
				"site: dev",
				"namespace: guardian-oci",
				zotTestImage,
				"domain: oci.guardianintelligence.org",
				"storagePath: /var/lib/guardian/zot",
				"publisherSecret:",
				"name: zot-publisher",
				"htpasswdKey: htpasswd",
				"gateway:",
				"name: edge",
				"namespace: gateway",
				"httpsSectionName: https-oci",
				"waitForRollout: true",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("OCIRegistry environment render missing %q", want)
				}
			}
		})
	}
}

func TestZotImageIsEnvironmentOnly(t *testing.T) {
	c := componentByName(t, "zot")
	if !c.pushOnly {
		t.Fatal("zot image should be consumed by the OCIRegistry environment XR")
	}
	if c.manifest != "" {
		t.Fatalf("zot component still has a direct manifest: %s", c.manifest)
	}
	if c.enabled == nil {
		t.Fatal("zot image push should stay gated by platform.oci.domain")
	}
}
