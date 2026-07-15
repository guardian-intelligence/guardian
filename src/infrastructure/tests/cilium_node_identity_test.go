package tests

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The root ingress controller runs hostNetwork, so its traffic reaches
// Cilium-managed endpoints with the reserved host/remote-node identities —
// never a pod identity. A fromEndpoints/toEndpoints/endpointSelector entry
// naming its pod labels can never match anything: as an ingress admit it
// silently default-denies the route for every remote node (the 2026-07-15
// postflight webhook outage), and as an egress policy it is inert. Admit
// node-identity traffic with fromEntities [host, remote-node] instead.
func TestCiliumPoliciesNeverSelectHostNetworkIngress(t *testing.T) {
	root := runfilePath("src/infrastructure/deployments")
	policies := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			if !strings.Contains(doc, "kind: CiliumNetworkPolicy") &&
				!strings.Contains(doc, "kind: CiliumClusterwideNetworkPolicy") {
				continue
			}
			policies++
			if strings.Contains(doc, "app.kubernetes.io/name: ingress-nginx") {
				t.Errorf("%s: a Cilium policy references the hostNetwork ingress controller's pod labels; its traffic is the host/remote-node entity and can only be admitted via fromEntities", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if policies < 10 {
		t.Fatalf("only %d Cilium policies found under %s — runfiles wiring or the policy layout changed", policies, root)
	}
}
