package tests

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"gopkg.in/yaml.v3"
)

func runfilePath(path string) string {
	resolved, err := runfiles.Rlocation("_main/" + path)
	if err == nil {
		return resolved
	}
	resolved, err = runfiles.Rlocation(path)
	if err == nil {
		return resolved
	}
	return path
}

func assertTextContains(t *testing.T, text, want, context string) {
	t.Helper()

	if !strings.Contains(text, want) {
		t.Fatalf("%s does not contain %q", context, want)
	}
}

func assertTextNotContains(t *testing.T, text, forbidden, context string) {
	t.Helper()

	if strings.Contains(text, forbidden) {
		t.Fatalf("%s contains forbidden text %q", context, forbidden)
	}
}

// walkYAMLFiles decodes every *.yaml under dir (recursively) and passes each
// file's documents to fn.
func walkYAMLFiles(t *testing.T, dir string, fn func(path string, docs []map[string]interface{})) {
	t.Helper()

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		fn(path, yamlDocs(t, path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(payload)
}

func singleYAMLDoc(t *testing.T, path string) map[string]interface{} {
	t.Helper()

	docs := yamlDocs(t, path)
	if len(docs) != 1 {
		t.Fatalf("%s: decoded %d YAML documents, want 1", path, len(docs))
	}
	return docs[0]
}

func yamlDocs(t *testing.T, path string) []map[string]interface{} {
	t.Helper()

	payload, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(payload))
	var docs []map[string]interface{}
	for {
		var doc map[string]interface{}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if len(doc) > 0 {
			docs = append(docs, doc)
		}
	}
	return docs
}

func findDoc(t *testing.T, docs []map[string]interface{}, kind, name string) map[string]interface{} {
	t.Helper()

	for _, doc := range docs {
		if stringValue(doc["kind"]) != kind {
			continue
		}
		metadata := mapValue(doc["metadata"])
		if stringValue(metadata["name"]) == name {
			return doc
		}
	}
	t.Fatalf("did not find %s/%s", kind, name)
	return nil
}

func assertNestedString(t *testing.T, m map[string]interface{}, want string, path ...string) {
	t.Helper()

	if got := stringValue(nestedValue(t, m, path...)); got != want {
		t.Fatalf("%s = %q, want %q", strings.Join(path, "."), got, want)
	}
}

func assertNestedBool(t *testing.T, m map[string]interface{}, want bool, path ...string) {
	t.Helper()

	got, ok := nestedValue(t, m, path...).(bool)
	if !ok {
		t.Fatalf("%s is not a bool", strings.Join(path, "."))
	}
	if got != want {
		t.Fatalf("%s = %v, want %v", strings.Join(path, "."), got, want)
	}
}

func nestedMap(t *testing.T, m map[string]interface{}, path ...string) map[string]interface{} {
	t.Helper()
	return mapValue(nestedValue(t, m, path...))
}

func nestedValue(t *testing.T, m map[string]interface{}, path ...string) interface{} {
	t.Helper()

	var current interface{} = m
	for _, segment := range path {
		next, ok := mapValue(current)[segment]
		if !ok {
			t.Fatalf("missing %s in %s", segment, strings.Join(path, "."))
		}
		current = next
	}
	return current
}

func mapValue(value interface{}) map[string]interface{} {
	if value == nil {
		return map[string]interface{}{}
	}
	m, ok := value.(map[string]interface{})
	if ok {
		return m
	}
	return map[string]interface{}{}
}

func sliceValue(value interface{}) []interface{} {
	if value == nil {
		return nil
	}
	s, ok := value.([]interface{})
	if ok {
		return s
	}
	return nil
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	s, ok := value.(string)
	if ok {
		return s
	}
	return ""
}
