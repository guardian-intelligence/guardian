package main

import (
	"strings"
	"testing"
)

func TestSLOAndSyntheticSiteManifests(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestHost(t, siteName)
			if site.SLO.PublicHTTP == nil {
				t.Fatal("public-http SLOProfile not loaded")
			}
			if site.SLO.PublicHTTP.Surface != "public-http" {
				t.Fatalf("SLOProfile surface = %q, want public-http", site.SLO.PublicHTTP.Surface)
			}
			if len(site.SLO.PublicHTTP.Apps) != 2 {
				t.Fatalf("SLOProfile app count = %d, want 2", len(site.SLO.PublicHTTP.Apps))
			}
			if len(site.Synthetic.PublicHTTPTargets) == 0 {
				t.Fatal("SyntheticCheck public HTTP targets not loaded")
			}
			if len(site.Aisucks.Watch) == 0 {
				t.Fatal("Aisucks health watch targets not derived from SyntheticCheck")
			}
			if len(site.Aisucks.WatchPages) == 0 {
				t.Fatal("Aisucks page watch targets not derived from SyntheticCheck")
			}
		})
	}
}

func TestSLOProfilePlatformRender(t *testing.T) {
	out := buildTestPlatformPackage(t)
	for _, want := range []string{
		"name: sloprofiles.platform.guardian.dev",
		"kind: SLOProfile",
		"name: syntheticchecks.platform.guardian.dev",
		"kind: SyntheticCheck",
		"name: function-environment-configs",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SLOProfile/SyntheticCheck platform render missing %q", want)
		}
	}
}

func TestSyntheticCheckRejectsBadTarget(t *testing.T) {
	site := &Host{Name: "dev"}
	site.EnvironmentBundle.Path = "environment.yaml"
	site.EnvironmentBundle.Raw = []byte(`apiVersion: platform.guardian.dev/v1alpha1
kind: SyntheticCheck
metadata:
  name: public-http-cross-site
spec:
  site: dev
  surface: public-http
  targets:
    - name: bad
      product: aisucks
      kind: page
      url: not-a-url
`)
	_, err := syntheticChecks(site)
	if err == nil || !strings.Contains(err.Error(), "absolute http(s)") {
		t.Fatalf("syntheticChecks error = %v, want absolute http(s) validation", err)
	}
}
