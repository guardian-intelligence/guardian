package main

import (
	"bytes"
	"fmt"
	"os"
	"text/template"
)

// renderManifest renders a component manifest template with the computed
// in-cluster image reference (mirror host + workspace-built digest) and the
// assembled site view, so per-site platform values come from the environment
// bundle instead of forked manifests.
func renderManifest(manifestPath, image string, site *Site) ([]byte, error) {
	return renderComponentManifest(component{manifest: manifestPath}, image, nil, site)
}

func renderComponentManifest(c component, image string, images map[string]string, site *Site) ([]byte, error) {
	path, err := resolveRepoInputPath(c.manifest)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	if c.rawManifest {
		return raw, nil
	}
	tmpl, err := template.New("manifest").Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	var buf bytes.Buffer
	data := struct {
		Image   string
		Images  map[string]string
		Site    *Site
		Service *publicHTTPServiceRender
	}{Image: image, Images: images, Site: site, Service: c.publicHTTPServiceRender(site)}
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	return buf.Bytes(), nil
}
