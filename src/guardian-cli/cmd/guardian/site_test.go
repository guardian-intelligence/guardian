package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalBootstrap = `site: test
cluster:
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

func TestLoadBootstrapValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{{
		name: "valid",
		body: minimalBootstrap,
	}, {
		name:    "requires site name",
		body:    strings.Replace(minimalBootstrap, "site: test\n", "", 1),
		wantErr: "site is required",
	}, {
		name:    "requires valid prefix",
		body:    strings.Replace(minimalBootstrap, "prefixLength: 31", "prefixLength: 64", 1),
		wantErr: "node.prefixLength must be 1-32",
	}, {
		name:    "requires patches",
		body:    strings.Replace(minimalBootstrap, "    - src/sites/dev/talos/patches/single-node.yaml\n", "", 1),
		wantErr: "talos.patches is required",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bootstrap.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := loadBootstrap(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("loadBootstrap: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("loadBootstrap error = %v; want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestEnvironmentValidation(t *testing.T) {
	base := func() (*Site, *Environment) {
		site := &Site{Name: "test"}
		site.Cluster.Name = "guardian-test"
		site.Node.Hostname = "test-w0"
		env := &Environment{}
		env.Site.Name = "test"
		env.Site.ClusterName = "guardian-test"
		env.Site.NodeHostname = "test-w0"
		return site, env
	}
	cases := []struct {
		name    string
		mutate  func(*Site, *Environment)
		wantErr string
	}{{
		name: "valid",
	}, {
		name: "site identity must match",
		mutate: func(_ *Site, env *Environment) {
			env.Site.Name = "other"
		},
		wantErr: "site.name",
	}, {
		name: "podNetwork requires gateway",
		mutate: func(site *Site, _ *Environment) {
			site.Aisucks.PodNetwork = true
		},
		wantErr: "products.aisucks.podNetwork requires gateway.enabled",
	}, {
		name: "company requires gateway",
		mutate: func(site *Site, _ *Environment) {
			site.Company.Domain = "guardianintelligence.org"
		},
		wantErr: "products.company.domain requires gateway.enabled",
	}, {
		name: "status monitor requires domains",
		mutate: func(site *Site, _ *Environment) {
			site.Status.Monitor = true
		},
		wantErr: "platform.status.monitor requires platform.status.domains",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site, env := base()
			if tc.mutate != nil {
				tc.mutate(site, env)
			}
			err := validateSite(site, "bootstrap.yaml", "environment.yaml", env)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSite: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateSite error = %v; want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
