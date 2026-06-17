package main

import (
	"bytes"
	"io"
	"testing"

	"gopkg.in/yaml.v3"
)

func buildTestEnvironmentBundle(site *Site, images map[string]string) ([]byte, error) {
	kubectl, err := kubectlPath()
	if err != nil {
		return nil, err
	}
	return buildEnvironmentKustomization(kubectl, site, images)
}

func buildTestPlatformPackage(t *testing.T) string {
	t.Helper()
	return buildTestKustomization(t, "src/crossplane/packages/guardian-platform")
}

func buildTestProductPackage(t *testing.T) string {
	t.Helper()
	return buildTestKustomization(t, "src/crossplane/packages/guardian-products")
}

func buildTestKustomization(t *testing.T, path string) string {
	t.Helper()
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	rendered, err := buildKustomization(kubectl, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

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
