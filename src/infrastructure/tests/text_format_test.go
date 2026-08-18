package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Holds every in-scope runfile to what tools/format/text/format.sh (the
// trailing-whitespace pass `aspect tidy` runs) would emit, so formatting
// drift fails `bazelisk test //...` instead of landing on main. The scope
// rules here mirror format.sh's find clauses and must stay a subset of them:
// a file this test flags that format.sh does not rewrite would fail CI with
// no `aspect tidy` fix.
func TestTextFormatConformance(t *testing.T) {
	root := repoRootFromRunfiles(t)

	checked := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(path), root), "/")
		if !textFormatScope(rel) {
			return nil
		}
		checked++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)
		if len(content) == 0 {
			return nil
		}
		for i, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
			if strings.TrimRight(line, " \t") != line {
				t.Errorf("%s:%d: trailing whitespace; run `aspect tidy`", rel, i+1)
			}
		}
		if !strings.HasSuffix(content, "\n") {
			t.Errorf("%s: missing trailing newline; run `aspect tidy`", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk runfiles: %v", err)
	}
	// Sweep-coverage canary: the corpus is whatever this go_test declares as
	// data, so a data prune that silently narrows the check trips here.
	if checked < 200 {
		t.Fatalf("only %d files in scope; runfiles coverage collapsed", checked)
	}
}

func textFormatScope(rel string) bool {
	ext := strings.TrimPrefix(filepath.Ext(rel), ".")
	switch {
	case strings.HasPrefix(rel, ".aspect/"):
		return ext == "axl"
	case strings.HasPrefix(rel, ".github/workflows/"):
		return ext == "yml" && !strings.Contains(strings.TrimPrefix(rel, ".github/workflows/"), "/")
	case strings.HasPrefix(rel, "docs/"):
		return ext == "md" || ext == "yaml"
	case strings.HasPrefix(rel, "src/infrastructure/"):
		return ext == "json" || ext == "tf" || ext == "yaml"
	case strings.HasPrefix(rel, "tools/"):
		return ext == "bazel" || ext == "bzl"
	case !strings.Contains(rel, "/"):
		return ext == "bazel" || ext == "md" || ext == "yaml" || ext == "yml" || ext == "json"
	}
	return false
}
