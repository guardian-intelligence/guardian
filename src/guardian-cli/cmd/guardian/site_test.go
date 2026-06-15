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
	base := func() (*Site, *Environment, *environmentConfigMetadata) {
		site := &Site{Name: "test"}
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
		mutate  func(*Site, *Environment, *environmentConfigMetadata)
		wantErr string
	}{{
		name: "valid",
	}, {
		name: "environment name must match bootstrap cluster",
		mutate: func(_ *Site, _ *Environment, meta *environmentConfigMetadata) {
			meta.Name = "guardian-other"
		},
		wantErr: "metadata.name",
	}, {
		name: "environment selector label must match bootstrap site",
		mutate: func(_ *Site, _ *Environment, meta *environmentConfigMetadata) {
			meta.Labels["guardian.dev/site"] = "other"
		},
		wantErr: "metadata.labels[guardian.dev/site]",
	}, {
		name: "site identity must match",
		mutate: func(_ *Site, env *Environment, _ *environmentConfigMetadata) {
			env.Site.Name = "other"
		},
		wantErr: "site.name",
	}, {
		name: "company requires gateway",
		mutate: func(site *Site, _ *Environment, _ *environmentConfigMetadata) {
			site.Company.Domain = "guardianintelligence.org"
		},
		wantErr: "products.company.domain requires gateway.enabled",
	}, {
		name: "aisucks requires gateway",
		mutate: func(site *Site, _ *Environment, _ *environmentConfigMetadata) {
			site.Aisucks.Domain = "aisucks.app"
		},
		wantErr: "products.aisucks.domain requires gateway.enabled",
	}, {
		name: "status monitor requires domains",
		mutate: func(site *Site, _ *Environment, _ *environmentConfigMetadata) {
			site.Status.Monitor = true
		},
		wantErr: "platform.status.monitor requires platform.status.domains",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site, env, meta := base()
			if tc.mutate != nil {
				tc.mutate(site, env, meta)
			}
			err := validateSite(site, "bootstrap.yaml", "environment.yaml", env, meta)
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

func TestCompanySiteSpecDerivesProbeURLs(t *testing.T) {
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
	site := &Site{Name: "dev"}
	site.Company.Domain = "dev.guardianintelligence.org"
	site.Company.WatchDomains = []string{"gamma.guardianintelligence.org"}
	if err := validateCompanySiteSpec(site, "environment.yaml", xr); err != nil {
		t.Fatal(err)
	}
	got := companyProbeURLs(site.Company.WatchDomains, xr.Routes)
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
