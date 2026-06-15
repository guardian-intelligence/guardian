package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
			sitePath, err := toolPath("_main/src/sites/" + tc.siteName + "/bootstrap.yaml")
			if err != nil {
				t.Fatalf("locate bootstrap.yaml: %v", err)
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
				`"htpasswd": {`,
				`"path": "/zot-auth/htpasswd"`,
				`"anonymousPolicy": ["read"]`,
				`"users": ["guardian-release"]`,
				`"actions": ["read", "create", "update"]`,
				"containerPort: 5000",
				"secretName: zot-publisher",
				"key: htpasswd",
				"kind: HTTPRoute",
				"sectionName: https-oci",
				"port: 5000",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("zot render missing %q", want)
				}
			}
			cfg := zotRenderedConfig(t, rendered)
			if cfg["distSpecVersion"] != "1.1.1" {
				t.Fatalf("distSpecVersion = %v, want 1.1.1", cfg["distSpecVersion"])
			}
		})
	}
}

func zotRenderedConfig(t *testing.T, manifest []byte) map[string]any {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data map[string]string `yaml:"data"`
		}
		if err := dec.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode zot manifest: %v", err)
		}
		if doc.Kind == "ConfigMap" && doc.Metadata.Name == "zot" {
			var cfg map[string]any
			if err := json.Unmarshal([]byte(doc.Data["config.json"]), &cfg); err != nil {
				t.Fatalf("zot config.json is invalid JSON: %v", err)
			}
			return cfg
		}
	}
	t.Fatal("zot ConfigMap not found")
	return nil
}
