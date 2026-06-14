package main

import (
	"strings"
	"testing"
)

func TestVMAlertRenderPinsCompanySiteRule(t *testing.T) {
	tmpl, err := toolPath("_main/src/infrastructure-components/vmalert/k8s/vmalert.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate vmalert manifest: %v", err)
	}
	sitePath, err := toolPath("_main/src/sites/dev/site.yaml")
	if err != nil {
		t.Fatalf("locate site.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderManifest(tmpl, "registry.guardian.internal/vmalert@sha256:deadbeef", site)
	if err != nil {
		t.Fatal(err)
	}
	decodeKinds(t, rendered)

	out := string(rendered)
	for _, want := range []string{
		"AppErrorRate",
		"aisucks_http_requests_total",
		"CompanySiteErrorRate",
		"company_site_http_requests_total",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("vmalert render missing %q", want)
		}
	}
}
