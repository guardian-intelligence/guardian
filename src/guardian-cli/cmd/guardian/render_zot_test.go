package main

import (
	"strings"
	"testing"
)

func TestZotRender(t *testing.T) {
	const image = "registry.guardian.internal/zot@sha256:deadbeef"
	c := componentByName(t, "zot")
	tmpl, err := toolPath("_main/src/infrastructure-components/zot/k8s/zot.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate zot manifest: %v", err)
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
					t.Fatalf("zot should render empty for %s, got:\n%s", tc.siteName, out)
				}
				return
			}
			for _, want := range []string{
				"namespace: guardian-oci",
				"name: zot",
				"type: Recreate",
				"image: " + image,
				`command: ["/usr/local/bin/zot-linux-amd64"]`,
				`"search": {`,
				`"ui": {`,
				`"enable": true`,
				`"port": "5000"`,
				"containerPort: 5000",
				"kind: HTTPRoute",
				"sectionName: https-oci",
				"port: 5000",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("zot render missing %q", want)
				}
			}
		})
	}
}
