package main

import (
	"strings"
	"testing"
)

// TestGatusProbeAlias pins the Gatus half of the dev-pilot prober fix:
// public-URL self-probes cannot hairpin through the hostNetwork Envoy
// listener — the kernel completes the TCP
// handshake but cilium-envoy never services a connection that did not
// arrive via Cilium's proxy redirect (upstream cilium/cilium#36004;
// measured live on dev, 100% of pod-originated probes timing out while
// external clients serve fine). The fix aliases the site domain to the
// pinned aisucks-probe ClusterIP so the same URLs keep measuring app +
// certificate (full TLS verification, real SNI) from in-cluster; the edge
// listener itself is the sibling watchers' job.
func TestGatusProbeAlias(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	c := componentByName(t, "gatus")
	const image = "registry.guardian.internal/gatus@sha256:deadbeef"
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestHost(t, siteName)
			rendered, err := buildComponentKustomization(kubectl, c, map[string]string{"gatus": image}, site)
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			decodeKinds(t, rendered) // structural validity

			if site.Aisucks.Domain != "" {
				for _, want := range []string{
					"hostAliases:",
					"ip: 10.96.111.43", // must match the aisucks-probe Service pin
					"- " + site.Aisucks.Domain,
				} {
					if !strings.Contains(out, want) {
						t.Errorf("pod-network gatus render missing %q", want)
					}
				}
				// The probe URLs themselves stay the public ones: the alias
				// redirects resolution, not semantics.
				if !strings.Contains(out, "https://"+site.Aisucks.Domain+"/healthz") {
					t.Error("self-probe URL must stay the public domain (hostAliases redirects resolution)")
				}
			}
		})
	}
}
