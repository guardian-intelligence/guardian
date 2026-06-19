package main

import (
	"strings"
	"testing"
)

const aisucksTestImage = "registry.guardian.internal/aisucks@sha256:deadbeef"
const statusTestImage = "registry.guardian.internal/status@sha256:deadbeef"
const victoriaMetricsTestImage = "registry.guardian.internal/victoria-metrics@sha256:deadbeef"

func TestAisucksProductAPIRender(t *testing.T) {
	out := buildTestProductPackage(t)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: aisucksproducts.products.guardian.dev",
		"kind: Composition",
		"name: aisucks-product-public-http",
		"kind: PublicHttpService",
		"name: aisucks",
		"namespace: aisucks",
		"app: aisucks",
		"domain: {{ $spec.domain }}",
		"image: {{ $spec.image }}",
		"podNetwork: true",
		"replicas: {{ $spec.replicas }}",
		"metrics: 9090",
		"diagnostics: \":9090\"",
		"tlsSectionName: https-aisucks",
		"tlsMode: Terminate",
		"httpRouteHostnames:",
		"- {{ $spec.domain }}",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("AisucksProduct API render missing %q", want)
		}
	}
}

func TestAisucksEnvironmentBundleInstances(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestHost(t, siteName)
			rendered, err := buildTestEnvironmentBundle(site, testProductImages())
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			for _, want := range []string{
				"kind: AisucksProduct",
				"name: aisucks",
				"site: " + siteName,
				"domain: " + site.Aisucks.Domain,
				aisucksTestImage,
				"replicas: 2",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("AisucksProduct environment render missing %q", want)
				}
			}
			if strings.Contains(out, "{{ index .Images") {
				t.Error("environment bundle left bootstrap image placeholders unresolved")
			}
		})
	}
}

func testHostPath(t *testing.T, environment string) string {
	t.Helper()
	hostDirByEnvironment := map[string]string{
		"dev":   "ash-bm-001",
		"gamma": "ash-bm-002",
		"prod":  "ash-bm-003",
	}
	hostDir, ok := hostDirByEnvironment[environment]
	if !ok {
		t.Fatalf("no checked-in host for environment %q", environment)
	}
	sitePath, err := toolPath("_main/src/hosts/" + hostDir + "/host.yaml")
	if err != nil {
		t.Fatalf("locate host.yaml: %v", err)
	}
	return sitePath
}

func loadTestHost(t *testing.T, siteName string) *Host {
	t.Helper()
	sitePath := testHostPath(t, siteName)
	site, err := loadHost(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func testProductImages() map[string]string {
	return map[string]string{
		"aisucks":          aisucksTestImage,
		"company-site":     companyTestImage,
		"directus":         directusTestImage,
		"postgres":         postgresTestImage,
		"status":           statusTestImage,
		"victoria-metrics": victoriaMetricsTestImage,
		"zot":              zotTestImage,
	}
}

func componentByName(t *testing.T, name string) component {
	t.Helper()
	for _, c := range components {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("component %q not found", name)
	return component{}
}
