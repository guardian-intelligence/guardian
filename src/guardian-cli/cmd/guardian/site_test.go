package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalSite is a structurally valid site.yaml body the flag-validation
// tests append to.
const minimalSite = `cluster:
  name: guardian-test
  endpoint: https://192.0.2.1:6443
node:
  address: 192.0.2.1
  hostname: test-w0
  prefixLength: 31
  gateway: 192.0.2.0
  interfaceMac: "00:00:5e:00:53:01"
  installDiskSerial: "TESTINSTALL"
  zfsDiskSerial: "TESTZFS"
talos:
  schematic: src/sites/dev/talos/schematic.yaml
  patches:
    - src/sites/dev/talos/patches/single-node.yaml
`

// TestLoadSiteFlagValidation pins the cross-flag invariants loadSite
// enforces: pod-network aisucks serves only through the Gateway's routes
// (without gateway.enabled nothing answers on host :80/:443), and
// status.monitor with no status.domains would render an empty blackbox
// target list.
func TestLoadSiteFlagValidation(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		wantErr string
	}{{
		name:  "defaults valid",
		extra: "",
	}, {
		name:    "podNetwork requires gateway",
		extra:   "aisucks:\n  podNetwork: true\n",
		wantErr: "aisucks.podNetwork requires gateway.enabled",
	}, {
		name:  "podNetwork with gateway",
		extra: "aisucks:\n  podNetwork: true\ngateway:\n  enabled: true\n",
	}, {
		name:    "monitor requires domains",
		extra:   "status:\n  monitor: true\n",
		wantErr: "status.monitor requires status.domains",
	}, {
		name:  "monitor with domains",
		extra: "status:\n  monitor: true\n  domains:\n    - status.example.org\n",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "site.yaml")
			if err := os.WriteFile(path, []byte(minimalSite+tc.extra), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadSite(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("loadSite: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("loadSite error = %v; want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
