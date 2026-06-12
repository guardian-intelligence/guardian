package main

import (
	"os"
	"testing"
)

// Live-canary checks, used by the release gate (docs/runbooks). Skipped
// unless pointed at material: CANARY_HTML names a saved share-page file,
// CANARY_URL fetches a live share link. Neither runs under bazel test.
func TestCanaryHTML(t *testing.T) {
	path := os.Getenv("CANARY_HTML")
	if path == "" {
		t.Skip("set CANARY_HTML to a saved share-page file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := parseChatGPT(string(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("model=%q turns=%d", conv.Model, len(conv.Turns))
	for i, turn := range conv.Turns {
		preview := turn.Content
		if len(preview) > 80 {
			preview = preview[:80]
		}
		t.Logf("  %d %s: %s", i, turn.Role, preview)
	}
	if len(conv.Turns) < 2 {
		t.Fatalf("expected at least a user+assistant pair, got %d turns", len(conv.Turns))
	}
}
