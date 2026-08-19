package wum

import (
	"os"
	"strings"
	"testing"
)

// game.conf is what the running host actually learns about WUM; this pins
// it to the package's own constants so the two can never drift apart.
func TestManifestMatchesVocabulary(t *testing.T) {
	raw, err := os.ReadFile("game.conf")
	if err != nil {
		t.Fatalf("game manifest: %v", err)
	}
	got := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		got[strings.Join(strings.Fields(line), " ")] = true
	}
	want := []string{
		"depart 2", // EvLeave
		"action 1 join",
		"action 3 check_in",
		"action 4 move_to",
		"action 8 boost",
		"reject 1 encoding",
		"reject 2 present",
		"reject 3 absent",
		"reject 4 full",
		"reject 5 checked_in",
		"reject 6 kind",
		"reject 7 epoch",
		"reject 8 target",
		"reject 9 terrain",
		"reject 10 noop",
		// Pre-v5 payload lengths for the dog-id-prefix kinds.
		"legacy_actor 1 8",
		"legacy_actor 2 8",
		"legacy_actor 3 8",
		"legacy_actor 4 10",
		"legacy_actor 8 9",
	}
	for _, dir := range want {
		if !got[dir] {
			t.Errorf("game.conf is missing %q", dir)
		}
		delete(got, dir)
	}
	for dir := range got {
		t.Errorf("game.conf has %q, which nothing here expects", dir)
	}
}
