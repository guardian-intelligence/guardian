package wum

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// game.conf is what the running host actually learns about WUM; this pins
// it to the package's own kind constants so the two can never drift. The
// names are literal because game.conf is their source of truth now — the
// numbers are what wum.go and the sim module own.
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
		fmt.Sprintf("depart %d", EvLeave),
		fmt.Sprintf("action %d join", EvJoin),
		fmt.Sprintf("action %d check_in", EvCheckIn),
		fmt.Sprintf("action %d move_to", EvMoveTo),
		fmt.Sprintf("action %d boost", EvBoostSet),
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
		fmt.Sprintf("legacy_actor %d 8", EvJoin),
		fmt.Sprintf("legacy_actor %d 8", EvLeave),
		fmt.Sprintf("legacy_actor %d 8", EvCheckIn),
		fmt.Sprintf("legacy_actor %d 10", EvMoveTo),
		fmt.Sprintf("legacy_actor %d 9", EvBoostSet),
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
