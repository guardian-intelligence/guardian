package main

import (
	"bytes"
	"fmt"
	"os"
	"text/template"
)

// renderManifest renders a component manifest template with the computed
// in-cluster image reference (mirror host + workspace-built digest) and the
// site, so per-site values (domains, feature toggles) come from site.yaml
// instead of forked manifests.
func renderManifest(manifestPath, image string, site *Site) ([]byte, error) {
	path := resolvePath(manifestPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	tmpl, err := template.New("manifest").Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	var buf bytes.Buffer
	data := struct {
		Image string
		Site  *Site
	}{Image: image, Site: site}
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	return buf.Bytes(), nil
}
