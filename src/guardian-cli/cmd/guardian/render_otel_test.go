package main

import (
	"strings"
	"testing"
)

// TestOtelAisucksScrapeBranch pins the prober co-change that MUST ride the
// same converge as aisucks.podNetwork: the collector's aisucks job scrapes
// host loopback (127.0.0.1:9090) under hostNetwork, and discovers pods
// (kubernetes_sd, <podIP>:9090, instance = pod name) once the app leaves
// the host netns — without the branch, up{job="aisucks"} goes 0 at the flip
// and ScrapeTargetDown pages 10 minutes later. The pods RBAC follows the
// same flag. It also pins the status.monitor gate: status hostnames join
// the blackbox targets only behind the (default-off) flag — pre-DNS they
// would emit probe_success == 0 and SiteProbeFailed pages on 2m of that.
func TestOtelAisucksScrapeBranch(t *testing.T) {
	tmpl, err := toolPath("_main/src/infrastructure-components/otel-collector/k8s/otel-collector.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate otel-collector manifest: %v", err)
	}
	const image = "registry.guardian.internal/otel-collector@sha256:deadbeef"
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + siteName + "/site.yaml")
			if err != nil {
				t.Fatalf("locate site.yaml: %v", err)
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

			if site.Aisucks.PodNetwork {
				for _, want := range []string{
					"kubernetes_sd_configs",
					"__meta_kubernetes_pod_label_app",
					"__meta_kubernetes_pod_name",
					"- pods", // RBAC resource for pod discovery
				} {
					if !strings.Contains(out, want) {
						t.Errorf("pod-network otel render missing %q", want)
					}
				}
				if strings.Contains(out, `targets: ["127.0.0.1:9090"]`) {
					t.Error("pod-network otel render must drop the static loopback aisucks target (it goes dark at the flip)")
				}
			} else {
				if !strings.Contains(out, `targets: ["127.0.0.1:9090"]`) {
					t.Error("hostNetwork otel render must keep the static loopback aisucks target")
				}
				for _, banned := range []string{"kubernetes_sd_configs", "- pods"} {
					if strings.Contains(out, banned) {
						t.Errorf("hostNetwork otel render must not contain %q", banned)
					}
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
