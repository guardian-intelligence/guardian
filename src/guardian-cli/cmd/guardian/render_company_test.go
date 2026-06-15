package main

import (
	"strings"
	"testing"
)

const (
	companyTestImage  = "registry.guardian.internal/company-site@sha256:deadbeef"
	directusTestImage = "registry.guardian.internal/directus@sha256:deadbeef"
	postgresTestImage = "registry.guardian.internal/postgres@sha256:deadbeef"
)

func TestPublicHTTPServicePlatformRender(t *testing.T) {
	c := componentByName(t, "public-http-service-platform")
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: publichttpservices.platform.guardian.dev",
		"kind: Composition",
		"name: public-http-service-kubernetes",
		"kind: Namespace",
		"kind: Deployment",
		"kind: Service",
		"kind: TLSRoute",
		"kind: HTTPRoute",
		"type: RollingUpdate",
		"maxUnavailable: 0",
		"maxSurge: 1",
		`platform.guardian.dev/metrics-scrape: "true"`,
		`platform.guardian.dev/metrics-port: "{{ $spec.ports.metrics }}"`,
		"platform.guardian.dev/slo-surface: public-http",
		"allowPrivilegeEscalation: false",
		"ENABLE_APP_TLS",
		"REDIRECT_HTTP",
		"RequestRedirect",
		"name: {{ $spec.app }}-http-redirect",
		"watch: true",
		"type: RuntimeDefault",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PublicHttpService platform render missing %q", want)
		}
	}
}

func TestDirectusPlatformRender(t *testing.T) {
	c := componentByName(t, "directus-platform")
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: directusinstances.platform.guardian.dev",
		"kind: Composition",
		"name: directus-instance-kubernetes",
		"kind: StatefulSet",
		"kind: Deployment",
		"kind: Service",
		"initContainers:",
		"{{- $replicas := 1 -}}",
		"replicas: {{ $replicas }}",
		"prepare-data-dir",
		"chown -R postgres:postgres /var/lib/postgresql/data",
		"wait-for-postgres",
		"pg_isready -h directus-postgres",
		"prepare-uploads-dir",
		"chown -R node:node /directus/uploads",
		"PUBLIC_URL",
		"STORAGE_LOCATIONS",
		"STORAGE_LOCAL_DRIVER",
		"STORAGE_S3_DRIVER",
		"STORAGE_S3_BUCKET",
		"STORAGE_S3_KEY",
		"STORAGE_S3_SECRET",
		"/server/ping",
		"/directus/uploads",
		"hostPath:",
		"name: function-environment-configs",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("DirectusInstance platform render missing %q", want)
		}
	}
}

func TestCompanySiteProductAPIRender(t *testing.T) {
	c := componentByName(t, "company-site-product-api")
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: companysites.products.guardian.dev",
		"kind: Composition",
		"name: company-site-public-http",
		"kind: PublicHttpService",
		"name: company-site",
		"namespace: company",
		"app: company-site",
		"domain: {{ $spec.domain }}",
		"image: {{ $spec.image }}",
		"podNetwork: true",
		"replicas: {{ $spec.replicas }}",
		"metrics: 9090",
		"diagnostics: \":9090\"",
		"tlsSectionName: https-company-site",
		"tlsMode: Terminate",
		"httpRouteHostnames:",
		"- {{ $spec.domain }}",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CompanySite API render missing %q", want)
		}
	}
}

func TestCompanySiteEnvironmentBundleInstances(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			rendered, err := renderEnvironmentBundle(site, testProductImages())
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			for _, want := range []string{
				"kind: CompanySite",
				"name: company-site",
				"site: " + siteName,
				"domain: " + site.Company.Domain,
				companyTestImage,
				"directusRef:",
				"name: directus",
				"contentSnapshot:",
				"digest: workspace",
				"- /letters",
				"- /news",
				"replicas: 2",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("CompanySite environment render missing %q", want)
				}
			}
		})
	}
}

func TestDirectusEnvironmentBundleInstances(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			rendered, err := renderEnvironmentBundle(site, testProductImages())
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			for _, want := range []string{
				"kind: DirectusInstance",
				"name: directus",
				"namespace: directus",
				directusTestImage,
				postgresTestImage,
				"publicAdminRoute: false",
				"waitForRollout: false",
				"runtimeSecretName: directus-runtime",
				"databaseSecretName: directus-postgres",
				"storagePath: /var/lib/guardian/directus/postgres",
				"uploadsPath: /var/lib/guardian/directus/uploads",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("DirectusInstance environment render missing %q", want)
				}
			}
			if siteName == "prod" {
				for _, want := range []string{
					"runtime:",
					"suspend: true",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("prod DirectusInstance environment render missing %q", want)
					}
				}
			}
		})
	}
}
