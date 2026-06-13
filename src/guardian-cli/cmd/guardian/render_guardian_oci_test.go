package main

import (
	"strings"
	"testing"
)

func TestGuardianOCIRender(t *testing.T) {
	const image = "registry.guardian.internal/guardian-oci@sha256:deadbeef"
	c := componentByName(t, "guardian-oci")
	tmpl, err := toolPath("_main/src/infrastructure-components/guardian-oci/k8s/guardian-oci.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate guardian-oci manifest: %v", err)
	}
	c.manifest = tmpl
	for _, tc := range []struct {
		siteName string
		want     bool
	}{
		{siteName: "dev", want: true},
		{siteName: "gamma", want: false},
		{siteName: "prod", want: false},
	} {
		t.Run(tc.siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + tc.siteName + "/site.yaml")
			if err != nil {
				t.Fatalf("locate site.yaml: %v", err)
			}
			site, err := loadSite(sitePath)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := renderComponentManifest(c, image, nil, site)
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			if !tc.want {
				if strings.TrimSpace(out) != "" {
					t.Fatalf("guardian-oci should render empty for %s, got:\n%s", tc.siteName, out)
				}
				return
			}
			for _, want := range []string{
				"namespace: guardian-oci",
				"name: oci-placeholder",
				"image: " + image,
				"containerPort: 8080",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("guardian-oci render missing %q", want)
				}
			}
		})
	}
}
