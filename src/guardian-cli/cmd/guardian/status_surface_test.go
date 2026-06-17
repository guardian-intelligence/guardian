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
			site := loadTestHost(t, siteName)
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
			if surface.Spec.Namespace != "status" {
				t.Fatalf("StatusSurface namespace = %q, want status", surface.Spec.Namespace)
			}
			if surface.Spec.Image == "" {
				t.Fatal("StatusSurface image is required")
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
	out := buildTestPlatformPackage(t)
	for _, want := range []string{
		"name: statussurfaces.platform.guardian.dev",
		"kind: StatusSurface",
		"name: status-surface-status",
		"kind: Object",
		"name: status-surface-{{ $spec.namespace }}-deployment",
		"image: {{ $spec.image }}",
		"name: status-surface-{{ $spec.namespace }}-tls-route",
		"sectionName: {{ $spec.gateway.tlsSectionNamePrefix }}-{{ $i }}",
		"domainCount",
		"name: function-environment-configs",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("StatusSurface platform render missing %q", want)
		}
	}
}

func TestStatusSurfaceEnvironmentBundleInstances(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestHost(t, siteName)
			rendered, err := buildTestEnvironmentBundle(site, testProductImages())
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			for _, want := range []string{
				"kind: StatusSurface",
				"name: status",
				"site: " + siteName,
				"namespace: status",
				statusTestImage,
				"certDir: /var/lib/status-certs",
				"victoriaMetricsURL: http://victoria-metrics.observability.svc:8428",
				"tlsSectionNamePrefix: tls-status",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("StatusSurface environment render missing %q", want)
				}
			}
			if strings.Contains(out, "{{ index .Images") {
				t.Error("environment bundle left bootstrap image placeholders unresolved")
			}
		})
	}
}

func TestStatusSurfaceRejectsMonitorWithoutDomains(t *testing.T) {
	site := &Host{Name: "dev"}
	site.EnvironmentBundle.Path = "environment.yaml"
	site.EnvironmentBundle.Raw = []byte(`apiVersion: platform.guardian.dev/v1alpha1
kind: StatusSurface
metadata:
  name: status
spec:
  site: dev
  namespace: status
  image: registry.guardian.internal/status@sha256:deadbeef
  domains: []
  monitor: true
  replicas: 0
  acmeEmail: ops@example.com
  certDir: /var/lib/status-certs
  victoriaMetricsURL: http://victoria-metrics.observability.svc:8428
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      memory: 512Mi
      goMemory: 410MiB
  gateway:
    name: edge
    namespace: gateway
    tlsRouteAPIVersion: gateway.networking.k8s.io/v1alpha2
    tlsSectionNamePrefix: tls-status
`)
	_, err := statusSurfaces(site)
	if err == nil || !strings.Contains(err.Error(), "spec.monitor requires spec.domains") {
		t.Fatalf("statusSurfaces error = %v, want monitor/domains validation", err)
	}
}
