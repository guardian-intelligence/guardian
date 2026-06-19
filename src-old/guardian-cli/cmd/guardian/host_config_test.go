package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalHost = `host: ash-bm-test
environment: test
provider:
  name: latitude
  serverId: sv_test
  metro: ash
  plan: f4.metal.small
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
storage:
  pools:
    - name: guardian
      type: zfs
      role: product-workloads
      deviceSerials:
        - TESTZFS
      wipePolicy: never
      mountpoint: /var/mnt/guardian
talos:
  schematic: src/hosts/ash-bm-001/talos/schematic.yaml
  patches:
    - src/hosts/ash-bm-001/talos/patches/single-node.yaml
`

func TestLoadHostConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{{
		name: "valid",
		body: minimalHost,
	}, {
		name:    "requires host id",
		body:    strings.Replace(minimalHost, "host: ash-bm-test\n", "", 1),
		wantErr: "host is required",
	}, {
		name:    "requires environment",
		body:    strings.Replace(minimalHost, "environment: test\n", "", 1),
		wantErr: "environment is required",
	}, {
		name:    "requires provider server id",
		body:    strings.Replace(minimalHost, "  serverId: sv_test\n", "", 1),
		wantErr: "provider.serverId is required",
	}, {
		name:    "requires valid prefix",
		body:    strings.Replace(minimalHost, "prefixLength: 31", "prefixLength: 64", 1),
		wantErr: "node.prefixLength must be 1-32",
	}, {
		name:    "requires patches",
		body:    strings.Replace(minimalHost, "    - src/hosts/ash-bm-001/talos/patches/single-node.yaml\n", "", 1),
		wantErr: "talos.patches is required",
	}, {
		name:    "requires storage pools",
		body:    strings.Replace(minimalHost, "storage:\n  pools:\n    - name: guardian\n      type: zfs\n      role: product-workloads\n      deviceSerials:\n        - TESTZFS\n      wipePolicy: never\n      mountpoint: /var/mnt/guardian\n", "", 1),
		wantErr: "storage.pools is required",
	}, {
		name:    "storage cannot use install disk",
		body:    strings.Replace(minimalHost, "- TESTZFS", "- TESTINSTALL", 1),
		wantErr: "must not include install disk",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "host.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := loadHostConfig(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("loadHostConfig: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("loadHostConfig error = %v; want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestEnvironmentValidation(t *testing.T) {
	base := func() (*Host, *Environment, *environmentConfigMetadata) {
		site := &Host{Name: "test"}
		site.Cluster.Name = "guardian-test"
		site.Node.Hostname = "test-w0"
		env := &Environment{}
		env.Site.Name = "test"
		env.Site.ClusterName = "guardian-test"
		env.Site.NodeHostname = "test-w0"
		meta := &environmentConfigMetadata{
			Name: "guardian-test",
			Labels: map[string]string{
				"guardian.dev/site": "test",
			},
		}
		return site, env, meta
	}
	cases := []struct {
		name    string
		mutate  func(*Host, *Environment, *environmentConfigMetadata)
		wantErr string
	}{{
		name: "valid",
	}, {
		name: "environment name must match host cluster",
		mutate: func(_ *Host, _ *Environment, meta *environmentConfigMetadata) {
			meta.Name = "guardian-other"
		},
		wantErr: "metadata.name",
	}, {
		name: "environment selector label must match host environment",
		mutate: func(_ *Host, _ *Environment, meta *environmentConfigMetadata) {
			meta.Labels["guardian.dev/site"] = "other"
		},
		wantErr: "metadata.labels[guardian.dev/site]",
	}, {
		name: "site identity must match",
		mutate: func(_ *Host, env *Environment, _ *environmentConfigMetadata) {
			env.Site.Name = "other"
		},
		wantErr: "site.name",
	}, {
		name: "company requires gateway",
		mutate: func(site *Host, _ *Environment, _ *environmentConfigMetadata) {
			site.Company.Domain = "guardianintelligence.org"
		},
		wantErr: "products.company.domain requires gateway.enabled",
	}, {
		name: "aisucks requires gateway",
		mutate: func(site *Host, _ *Environment, _ *environmentConfigMetadata) {
			site.Aisucks.Domain = "aisucks.app"
		},
		wantErr: "products.aisucks.domain requires gateway.enabled",
	}, {
		name: "status monitor requires domains",
		mutate: func(site *Host, _ *Environment, _ *environmentConfigMetadata) {
			site.Status.Monitor = true
		},
		wantErr: "StatusSurface spec.monitor requires spec.domains",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site, env, meta := base()
			if tc.mutate != nil {
				tc.mutate(site, env, meta)
			}
			err := validateHostEnvironment(site, "host.yaml", "environment.yaml", env, meta)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateHostEnvironment: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateHostEnvironment error = %v; want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestCompanyProbeURLs(t *testing.T) {
	raw := []byte(`apiVersion: apiextensions.crossplane.io/v1beta1
kind: EnvironmentConfig
metadata:
  name: guardian-dev
---
apiVersion: products.guardian.dev/v1alpha1
kind: CompanySite
metadata:
  name: company-site
spec:
  site: dev
  domain: dev.guardianintelligence.org
  image: registry.guardian.internal/company-site@sha256:deadbeef
  routes:
    - /
    - /letters
    - /news
  replicas: 2
`)
	xr, err := loadCompanySiteSpec(raw, "environment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	site := &Host{Name: "dev"}
	site.Company.Domain = "dev.guardianintelligence.org"
	if err := validateCompanySiteSpec(site, "environment.yaml", xr); err != nil {
		t.Fatal(err)
	}
	got := companyProbeURLs([]string{"gamma.guardianintelligence.org"}, xr.Routes)
	want := []string{
		"https://gamma.guardianintelligence.org/healthz",
		"https://gamma.guardianintelligence.org/",
		"https://gamma.guardianintelligence.org/letters",
		"https://gamma.guardianintelligence.org/news",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("companyProbeURLs = %#v, want %#v", got, want)
	}
}
