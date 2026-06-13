package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestStatusManifestGuard renders the status component's manifest for each
// real site.yaml and pins the deployment guard: a site with status.domains
// gets the Namespace/Deployment/Service trio (with the Service selector
// matching the pod labels — it is the TLSRoute backendRef), and a site
// without (prod) renders nothing at all, which the apply loop skips.
func TestStatusManifestGuard(t *testing.T) {
	tmpl, err := toolPath("_main/src/status/k8s/status.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate status manifest: %v", err)
	}
	const image = "registry.guardian.internal/status@sha256:deadbeef"

	wantDomains := map[string]string{
		"dev":   "status.guardianintelligence.org,status.dev.guardianintelligence.org",
		"gamma": "status.gamma.guardianintelligence.org",
		"prod":  "",
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
			rendered, err := renderManifest(tmpl, image, site)
			if err != nil {
				t.Fatal(err)
			}
			if wantDomains[siteName] == "" {
				if len(bytes.TrimSpace(rendered)) != 0 {
					t.Fatalf("site %s has no status.domains but the manifest rendered non-empty:\n%s", siteName, rendered)
				}
				return
			}
			if !strings.Contains(string(rendered), `value: "`+wantDomains[siteName]+`"`) {
				t.Errorf("rendered manifest missing DOMAINS %q", wantDomains[siteName])
			}
			if !strings.Contains(string(rendered), "image: "+image) {
				t.Errorf("rendered manifest missing image %s", image)
			}
			kinds := decodeKinds(t, rendered)
			for _, want := range []string{"Namespace", "Deployment", "Service"} {
				if !strings.Contains(strings.Join(kinds, ","), want) {
					t.Errorf("rendered manifest kinds = %v; missing %s", kinds, want)
				}
			}
		})
	}
}

// decodeKinds parses every YAML document and returns the object kinds —
// the cheap structural validity check (a template typo fails the decode).
func decodeKinds(t *testing.T, manifest []byte) []string {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	var kinds []string
	for {
		var doc struct {
			Kind string `yaml:"kind"`
		}
		if err := dec.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("rendered manifest is not valid YAML: %v\n%s", err, manifest)
		}
		if doc.Kind != "" {
			kinds = append(kinds, doc.Kind)
		}
	}
	return kinds
}
