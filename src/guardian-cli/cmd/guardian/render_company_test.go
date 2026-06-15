package main

import (
	"strings"
	"testing"
)

const companyTestImage = "registry.guardian.internal/company-site@sha256:deadbeef"

func TestCompanyPublicHTTPServiceRender(t *testing.T) {
	c := componentByName(t, "company-site")
	tmpl, err := toolPath("_main/" + c.manifest)
	if err != nil {
		t.Fatalf("locate public HTTP service manifest: %v", err)
	}
	c.manifest = tmpl

	sitePath, err := toolPath("_main/src/sites/dev/bootstrap.yaml")
	if err != nil {
		t.Fatalf("locate bootstrap.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	site.Company.Domain = "guardianintelligence.org"

	rendered, err := renderComponentManifest(c, companyTestImage, nil, site)
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: Namespace",
		"name: company",
		"kind: Deployment",
		"name: company-site",
		"image: " + companyTestImage,
		`value: "guardianintelligence.org"`,
		`value: /var/lib/company-site-certs`,
		"replicas: 2",
		"platform.guardian.dev/network: pod",
		`platform.guardian.dev/metrics-scrape: "true"`,
		`platform.guardian.dev/metrics-port: "9090"`,
		"platform.guardian.dev/slo-surface: public-http",
		"name: company-site-probe",
		"clusterIP: 10.96.111.44",
		"kind: TLSRoute",
		"sectionName: tls-company-site",
		"kind: HTTPRoute",
		"name: company-site-http",
		"hostnames:\n    - \"guardianintelligence.org\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
	kinds := strings.Join(decodeKinds(t, rendered), ",")
	if kinds != "Namespace,Deployment,Service,Service,TLSRoute,HTTPRoute" {
		t.Errorf("kinds = %s; want Namespace,Deployment,Service,Service,TLSRoute,HTTPRoute", kinds)
	}
	if strings.Contains(out, "hostNetwork: true") {
		t.Error("company site must stay pod-network only")
	}
}
