package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestOtelPublicHttpScrape pins the generic Server -> VictoriaMetrics path:
// pod-network PublicHttpService workloads opt in with bounded labels, the
// collector discovers those pods, and metrics flow through the prometheus
// receiver to VictoriaMetrics. The test also pins the status.monitor gate:
// status hostnames join blackbox targets only behind the default-off flag.
func TestOtelPublicHttpScrape(t *testing.T) {
	tmpl, err := toolPath("_main/src/infrastructure-components/otel-collector/k8s/otel-collector.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate otel-collector manifest: %v", err)
	}
	const image = "registry.guardian.internal/otel-collector@sha256:deadbeef"
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + siteName + "/bootstrap.yaml")
			if err != nil {
				t.Fatalf("locate bootstrap.yaml: %v", err)
			}
			site, err := loadSite(sitePath)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := renderManifest(tmpl, image, site)
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			decodeKinds(t, rendered) // structural validity

			for _, want := range []string{
				"job_name: public-http",
				"kubernetes_sd_configs",
				"guardian.dev/render-sha256:",
				"__meta_kubernetes_pod_label_platform_guardian_dev_metrics_scrape",
				"__meta_kubernetes_pod_label_platform_guardian_dev_metrics_port",
				"__meta_kubernetes_pod_label_platform_guardian_dev_slo_surface",
				"target_label: slo_surface",
				"- pods", // RBAC resource for pod discovery
			} {
				if !strings.Contains(out, want) {
					t.Errorf("otel render missing public-http scrape primitive %q", want)
				}
			}
			for _, target := range site.Company.ProbeURLs {
				if !strings.Contains(out, `- "`+target+`"`) {
					t.Errorf("otel render missing company blackbox target %q", target)
				}
			}
			if len(site.Company.WatchDomains) == 0 {
				if strings.Contains(out, "guardianintelligence.org/letters") {
					t.Errorf("site %s must not self-probe company routes through blackbox", siteName)
				}
			} else if len(site.Company.ProbeURLs) == 0 {
				t.Errorf("site %s must derive company blackbox targets from watchDomains and the CompanySite XR", siteName)
			}
			if !regexp.MustCompile(`guardian\.dev/render-sha256: "[0-9a-f]{64}"`).MatchString(out) {
				t.Error("otel render must include a render hash pod-template annotation so ConfigMap changes roll the collector")
			}

			// status.monitor is OFF fleet-wide until the status hostnames
			// resolve; no status domain may appear as a probe target.
			if site.Status.Monitor {
				t.Fatalf("site %s sets status.monitor — pre-DNS this is a guaranteed SiteProbeFailed page; flipping it is a deliberate post-DNS act", siteName)
			}
			for _, d := range site.Status.Domains {
				if strings.Contains(out, "https://"+d+"/healthz") {
					t.Errorf("status hostname %s in blackbox targets with status.monitor off", d)
				}
			}
		})
	}
}
