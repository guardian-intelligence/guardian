package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGatewayManifestRender renders the manifest-only gateway component for
// each real site.yaml and pins the route topology:
//
//   - the Gateway (namespace gateway) carries the :443 TLS-passthrough and
//     :80 HTTP listeners;
//   - one TLSRoute per SNI surface, at v1alpha2 — the pin of record: Cilium
//     1.19 consumes TLSRoute at v1alpha2 only (see the header of
//     gateway-api-crds-inline.yaml); a v1 TLSRoute would be silently
//     ignored;
//   - the aisucks HTTPRoute has NO hostnames: :80 must answer raw-IP Host
//     headers (the gatus no-domain fallback and ACME HTTP-01 / operator
//     curls must not depend on Host);
//   - status TLSRoutes exist exactly where site.yaml declares
//     status.domains (dev, gamma — not prod);
//   - the template never references .Image: it renders with the empty
//     image a manifest-only component gets.
func TestGatewayManifestRender(t *testing.T) {
	tmpl, err := toolPath("_main/src/infrastructure-components/gateway/k8s/gateway.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate gateway manifest: %v", err)
	}
	wantStatusDomains := map[string][]string{
		"dev":   {"status.guardianintelligence.org", "status.dev.guardianintelligence.org"},
		"gamma": {"status.gamma.guardianintelligence.org"},
		"prod":  nil,
	}
	wantAisucksDomain := map[string]string{
		"dev":   "dev.aisucks.app",
		"gamma": "gamma.aisucks.app",
		"prod":  "aisucks.app",
	}
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
			// Manifest-only: rendered with the zero image, like up.go does.
			rendered, err := renderManifest(tmpl, "", site)
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			if strings.Contains(out, "image:") {
				t.Error("gateway manifest must never reference an image: it is manifest-only")
			}

			docs := decodeGatewayDocs(t, rendered)
			wantKinds := []string{"Namespace", "Gateway", "TLSRoute", "HTTPRoute"}
			if len(wantStatusDomains[siteName]) > 0 {
				wantKinds = append(wantKinds, "TLSRoute")
			}
			var kinds []string
			for _, d := range docs {
				kinds = append(kinds, d.Kind)
			}
			if strings.Join(kinds, ",") != strings.Join(wantKinds, ",") {
				t.Fatalf("kinds = %v; want %v", kinds, wantKinds)
			}

			for _, d := range docs {
				switch {
				case d.Kind == "Gateway":
					if d.APIVersion != "gateway.networking.k8s.io/v1" {
						t.Errorf("Gateway apiVersion = %s; want v1", d.APIVersion)
					}
				case d.Kind == "TLSRoute":
					// THE CRD pin: Cilium 1.19 watches TLSRoute at v1alpha2.
					if d.APIVersion != "gateway.networking.k8s.io/v1alpha2" {
						t.Errorf("TLSRoute %s/%s apiVersion = %s; want v1alpha2 (Cilium 1.19 silently ignores v1)", d.Metadata.Namespace, d.Metadata.Name, d.APIVersion)
					}
				case d.Kind == "HTTPRoute":
					if len(d.Spec.Hostnames) != 0 {
						t.Errorf("HTTPRoute must stay hostname-less (raw-IP Host headers: gatus fallback, HTTP-01); got %v", d.Spec.Hostnames)
					}
				}
				if d.Kind == "TLSRoute" && d.Metadata.Name == "aisucks" {
					if len(d.Spec.Hostnames) != 1 || d.Spec.Hostnames[0] != wantAisucksDomain[siteName] {
						t.Errorf("aisucks TLSRoute hostnames = %v; want [%s]", d.Spec.Hostnames, wantAisucksDomain[siteName])
					}
				}
				if d.Kind == "TLSRoute" && d.Metadata.Name == "status" {
					if strings.Join(d.Spec.Hostnames, ",") != strings.Join(wantStatusDomains[siteName], ",") {
						t.Errorf("status TLSRoute hostnames = %v; want %v", d.Spec.Hostnames, wantStatusDomains[siteName])
					}
					if d.Metadata.Namespace != "status" {
						t.Errorf("status TLSRoute namespace = %s; want status (same-namespace backendRef, no ReferenceGrant)", d.Metadata.Namespace)
					}
				}
			}
		})
	}
}

// gatewayDoc is the slice of each rendered object the render test asserts.
type gatewayDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Hostnames []string `yaml:"hostnames"`
	} `yaml:"spec"`
}

func decodeGatewayDocs(t *testing.T, manifest []byte) []gatewayDoc {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	var docs []gatewayDoc
	for {
		var d gatewayDoc
		if err := dec.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("rendered gateway manifest is not valid YAML: %v\n%s", err, manifest)
		}
		if d.Kind != "" {
			docs = append(docs, d)
		}
	}
	return docs
}
