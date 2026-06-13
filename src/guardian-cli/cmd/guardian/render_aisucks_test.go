package main

import (
	"strings"
	"testing"
)

const aisucksTestImage = "registry.guardian.internal/aisucks@sha256:deadbeef"

func TestAisucksPublicHTTPServiceRender(t *testing.T) {
	c := componentByName(t, "aisucks")
	tmpl, err := toolPath("_main/" + c.manifest)
	if err != nil {
		t.Fatalf("locate public HTTP service manifest: %v", err)
	}
	c.manifest = tmpl
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
			rendered, err := renderComponentManifest(c, aisucksTestImage, site)
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)

			for _, want := range []string{
				"kind: Namespace",
				"name: aisucks",
				"kind: Deployment",
				"kind: Service",
				"image: " + aisucksTestImage,
				`name: DOMAIN`,
				`value: "` + site.Aisucks.Domain + `"`,
				`value: /var/lib/aisucks-certs`,
				"containerPort: 9090",
				"path: /healthz",
				"allowPrivilegeEscalation: false",
				"drop:\n                - ALL",
				"add:\n                - NET_BIND_SERVICE",
				"type: RuntimeDefault",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("render missing %q", want)
				}
			}

			kinds := decodeKinds(t, rendered)
			wantKinds := "Namespace,Deployment,Service"
			if site.Aisucks.PodNetwork {
				wantKinds = "Namespace,Deployment,Service,Service"
			}
			if got := strings.Join(kinds, ","); got != wantKinds {
				t.Errorf("kinds = %s; want %s", got, wantKinds)
			}

			if site.Aisucks.PodNetwork {
				for _, want := range []string{
					"replicas: 2",
					"platform.guardian.dev/network: pod",
					`value: ":8080"`,
					`value: ":8443"`,
					`value: ":9090"`,
					"name: aisucks-probe",
					"clusterIP: 10.96.111.43",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("pod-network render missing %q", want)
					}
				}
				for _, banned := range []string{
					"hostNetwork: true",
					"ClusterFirstWithHostNet",
					`value: "127.0.0.1:9090"`,
				} {
					if strings.Contains(out, banned) {
						t.Errorf("pod-network render must not contain %q", banned)
					}
				}
				if strings.Contains(out, "type: Recreate") {
					t.Error("pod-network render must not use the host-network Recreate strategy")
				}
			} else {
				for _, want := range []string{
					"replicas: 1",
					"type: Recreate",
					"hostNetwork: true",
					"ClusterFirstWithHostNet",
					`value: ":80"`,
					`value: ":443"`,
					`value: "127.0.0.1:9090"`,
				} {
					if !strings.Contains(out, want) {
						t.Errorf("host-network render missing %q", want)
					}
				}
				if strings.Contains(out, "aisucks-probe") {
					t.Error("host-network render must not create the pod-network probe Service")
				}
			}
		})
	}
}

func componentByName(t *testing.T, name string) component {
	t.Helper()
	for _, c := range components {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("component %q not found", name)
	return component{}
}
