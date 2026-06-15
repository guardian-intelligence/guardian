package main

import (
	"strings"
	"testing"
)

// TestOtelPublicHttpScrape pins the generic Server -> VictoriaMetrics path:
// pod-network PublicHttpService workloads opt in with bounded labels, the
// collector discovers those pods, and metrics flow through the prometheus
// receiver to VictoriaMetrics. Legacy host-network aisucks keeps its loopback
// scrape until it moves behind the generic public-http job. The test also pins
// the status.monitor gate: status hostnames join blackbox targets only behind
// the default-off flag.
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

			if site.Aisucks.PodNetwork {
				if strings.Contains(out, `targets: ["127.0.0.1:9090"]`) {
					t.Error("pod-network otel render must drop the static loopback aisucks target (it goes dark at the flip)")
				}
			} else {
				if !strings.Contains(out, `targets: ["127.0.0.1:9090"]`) {
					t.Error("hostNetwork otel render must keep the static loopback aisucks target")
				}
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
