package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	service := c.publicHTTPServiceRender(site)
	renderHash, err := renderedInputHash(c.manifest, raw, image, images, site, service)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	var buf bytes.Buffer
	data := struct {
		Image      string
		Images     map[string]string
		RenderHash string
		Site       *Site
		Service    *publicHTTPServiceRender
	}{Image: image, Images: images, RenderHash: renderHash, Site: site, Service: service}
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	return buf.Bytes(), nil
}

func renderedInputHash(manifest string, raw []byte, image string, images map[string]string, site *Site, service *publicHTTPServiceRender) (string, error) {
	input := struct {
		Manifest string
		Raw      []byte
		Image    string
		Images   map[string]string
		Site     *Site
		Service  *publicHTTPServiceRender
	}{
		Manifest: manifest,
		Raw:      raw,
		Image:    image,
		Images:   images,
		Site:     site,
		Service:  service,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("hash render inputs: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
