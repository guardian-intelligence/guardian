package main

import (
	"bytes"
	"io"
	"testing"

	"gopkg.in/yaml.v3"
)

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
