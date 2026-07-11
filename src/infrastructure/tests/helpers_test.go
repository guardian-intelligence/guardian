package tests

import (
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
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
