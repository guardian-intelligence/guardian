package main

import (
	"strings"
	"testing"
)

func TestStatusSurfaceSiteManifests(t *testing.T) {
	wantDomains := map[string][]string{
		"dev":   {"status.guardianintelligence.org", "status.dev.guardianintelligence.org"},
		"gamma": {"status.gamma.guardianintelligence.org"},
		"prod":  nil,
	}
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			surfaces, err := statusSurfaces(site)
			if err != nil {
				t.Fatal(err)
			}
			if len(surfaces) != 1 {
				t.Fatalf("status surface count = %d, want 1", len(surfaces))
			}
			surface := surfaces[0]
			if surface.Metadata.Name != "status" {
				t.Fatalf("StatusSurface name = %q, want status", surface.Metadata.Name)
			}
			if surface.Spec.Site != siteName {
				t.Fatalf("StatusSurface site = %q, want %q", surface.Spec.Site, siteName)
			}
			if strings.Join(surface.Spec.Domains, ",") != strings.Join(wantDomains[siteName], ",") {
				t.Fatalf("StatusSurface domains = %#v, want %#v", surface.Spec.Domains, wantDomains[siteName])
			}
			if strings.Join(site.Status.Domains, ",") != strings.Join(surface.Spec.Domains, ",") {
				t.Fatalf("site status domains = %#v, want StatusSurface domains %#v", site.Status.Domains, surface.Spec.Domains)
			}
			if site.Status.Monitor != surface.Spec.Monitor {
				t.Fatalf("site status monitor = %v, want StatusSurface value %v", site.Status.Monitor, surface.Spec.Monitor)
			}
		})
	}
}

func TestStatusSurfacePlatformRender(t *testing.T) {
	c := componentByName(t, "status-surface-platform")
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"name: statussurfaces.platform.guardian.dev",
		"kind: StatusSurface",
		"name: status-surface-status",
		"name: function-environment-configs",
		"name: function-auto-ready",
		"domainCount",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("StatusSurface platform render missing %q", want)
		}
	}
}

func TestStatusSurfaceRejectsMonitorWithoutDomains(t *testing.T) {
	site := &Site{Name: "dev"}
	site.EnvironmentBundle.Path = "environment.yaml"
	site.EnvironmentBundle.Raw = []byte(`apiVersion: platform.guardian.dev/v1alpha1
kind: StatusSurface
metadata:
  name: status
spec:
  site: dev
  domains: []
  monitor: true
`)
	_, err := statusSurfaces(site)
	if err == nil || !strings.Contains(err.Error(), "spec.monitor requires spec.domains") {
		t.Fatalf("statusSurfaces error = %v, want monitor/domains validation", err)
	}
}
