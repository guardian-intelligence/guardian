package main

import (
	"strings"
	"testing"
)

const zotTestImage = "registry.guardian.internal/zot@sha256:deadbeef"

func TestOCIRegistryPlatformRender(t *testing.T) {
	out := buildTestPlatformPackage(t)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: ociregistries.platform.guardian.dev",
		"kind: Composition",
		"name: oci-registry-kubernetes",
		"kind: SecretProjection",
		"name: {{ $xr.metadata.name }}-publisher-secrets",
		"namespaceLabels:",
		"role: {{ $spec.secrets.projection.openbao.role }}",
		"serviceAccountName: {{ $spec.secrets.projection.openbao.serviceAccountName }}",
		"remotePath: {{ $spec.secrets.projection.remotePath }}",
		"kind: Namespace",
		"kind: ConfigMap",
		"kind: PersistentVolumeClaim",
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
		"persistentVolumeClaim:",
		"claimName: {{ $spec.persistence.claimName }}",
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
			rendered, err := buildTestEnvironmentBundle(site, testProductImages())
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
				"namespaceLabels:",
				"pod-security.kubernetes.io/enforce: privileged",
				zotTestImage,
				"domain: oci.guardianintelligence.org",
				"claimName: zot-storage",
				"storageClassName: guardian-local-retain",
				"volumeName: guardian-dev-zot",
				"publisherSecret:",
				"name: zot-publisher",
				"usernameKey: username",
				"passwordKey: password",
				"htpasswdKey: htpasswd",
				"secrets:",
				"role: guardian-oci-secrets",
				"serviceAccountName: external-secrets-guardian-oci",
				"remotePath: guardian/guardian-dev/oci/zot-publisher",
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
	if c.enabled == nil {
		t.Fatal("zot image push should stay gated by platform.oci.domain")
	}
}
