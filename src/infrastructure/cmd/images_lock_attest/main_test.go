package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

const fixtureLock = `# fixture header, not a section
# purely decorative comment block

# --- section one ---
ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
ghcr.io/example/app2@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

# --- section two ---
# a comment inside section two, not an entry
ghcr.io/example/chart@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
`

func TestSectionCountsMatchFixture(t *testing.T) {
	counts, total, err := countSectionsAndEntries(fixtureLock)
	if err != nil {
		t.Fatalf("countSectionsAndEntries() error = %v", err)
	}
	want := map[string]int{
		"section one": 2,
		"section two": 1,
	}
	if len(counts) != len(want) {
		t.Fatalf("countSectionsAndEntries() sections = %v, want %v", counts, want)
	}
	for section, count := range want {
		if counts[section] != count {
			t.Fatalf("countSectionsAndEntries()[%q] = %d, want %d", section, counts[section], count)
		}
	}
	if total != 3 {
		t.Fatalf("countSectionsAndEntries() total = %d, want 3 (comment-only and blank lines must not be counted)", total)
	}
}

func TestCountSectionsAndEntriesRejectsEntryBeforeAnySection(t *testing.T) {
	_, _, err := countSectionsAndEntries("ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")
	if err == nil {
		t.Fatalf("countSectionsAndEntries() accepted an entry with no preceding section header")
	}
	if !strings.Contains(err.Error(), "before any section header") {
		t.Fatalf("countSectionsAndEntries() error = %v, want section-header detail", err)
	}
}

func TestCountSectionsAndEntriesRejectsUnpinnedEntry(t *testing.T) {
	_, _, err := countSectionsAndEntries("# --- section ---\nghcr.io/example/app:v1.0.0\n")
	if err == nil {
		t.Fatalf("countSectionsAndEntries() accepted a tag-only, non-digest-pinned entry")
	}
	if !strings.Contains(err.Error(), "not digest-pinned") {
		t.Fatalf("countSectionsAndEntries() error = %v, want pin detail", err)
	}
}

// This is the load-bearing regression check that the attestation's
// section-counting logic and the Tier-1 conformance suite's entry-counting
// logic (splitImageRef / parseImagesLock in
// src/infrastructure/tests/images_lock_test.go) never silently diverge on
// the real production lock. It re-derives the entry count via an
// independent, minimal pass (a bare "does this line contain a valid
// ref@sha256:<hex64>" regex, with no section-awareness at all) and asserts
// all three counting strategies agree.
func TestPredicateOmitsNothingFromRealLock(t *testing.T) {
	path := productionLockPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read production images.lock: %v", err)
	}

	sectionCounts, total, err := countSectionsAndEntries(string(raw))
	if err != nil {
		t.Fatalf("countSectionsAndEntries(production lock) error = %v", err)
	}

	sum := 0
	for _, count := range sectionCounts {
		sum += count
	}
	if sum != total {
		t.Fatalf("sum(sectionCounts.values()) = %d, totalEntries = %d; the attestation predicate's own fields disagree", sum, total)
	}

	independent := independentEntryCount(t, string(raw))
	if independent != total {
		t.Fatalf("independent entry count = %d, countSectionsAndEntries total = %d; the two counting strategies diverged", independent, total)
	}

	if total < 100 {
		t.Fatalf("production lock projected only %d entries; expected the full inventory (anti-vacuity floor 100)", total)
	}
	if len(sectionCounts) < 4 {
		t.Fatalf("production lock has %d sections, want at least the 4 documented in docs/images-lock-spec.md", len(sectionCounts))
	}
}

var independentEntryPattern = regexp.MustCompile(`^[^\s"']+@sha256:[a-f0-9]{64}$`)

// independentEntryCount counts entry lines with no notion of sections at
// all, deliberately reimplemented from scratch (not calling
// countSectionsAndEntries or validateEntry) so it cannot share a bug with
// the code under test.
func independentEntryCount(t *testing.T, raw string) int {
	t.Helper()
	count := 0
	for _, line := range strings.Split(raw, "\n") {
		entry := line
		if idx := strings.Index(entry, "#"); idx >= 0 {
			entry = entry[:idx]
		}
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !independentEntryPattern.MatchString(entry) {
			t.Fatalf("line %q is neither blank/comment nor a well-formed entry", line)
		}
		count++
	}
	return count
}

// TestPredicateJSONShape is a golden-shape test: an accidental field rename
// must not silently break cosign attest-blob's --predicate consumer or any
// downstream verifier expecting exactly these four keys.
func TestPredicateJSONShape(t *testing.T) {
	sectionCounts, total, err := countSectionsAndEntries(fixtureLock)
	if err != nil {
		t.Fatalf("countSectionsAndEntries() error = %v", err)
	}
	payload, err := json.Marshal(predicate{
		GitCommit:     "deadbeef",
		LockSHA256:    strings.Repeat("a", 64),
		SectionCounts: sectionCounts,
		TotalEntries:  total,
	})
	if err != nil {
		t.Fatalf("json.Marshal(predicate) error = %v", err)
	}

	var generic map[string]interface{}
	if err := json.Unmarshal(payload, &generic); err != nil {
		t.Fatalf("predicate JSON does not unmarshal: %v", err)
	}

	var keys []string
	for key := range generic {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"gitCommit", "lockSha256", "sectionCounts", "totalEntries"}
	if len(keys) != len(want) {
		t.Fatalf("predicate JSON keys = %v, want exactly %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("predicate JSON keys = %v, want exactly %v", keys, want)
		}
	}
}

func productionLockPath(t *testing.T) string {
	t.Helper()
	path, err := runfiles.Rlocation("_main/src/infrastructure/bootstrap/bundle/images.lock")
	if err == nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return path
		}
	}
	path, err = runfiles.Rlocation("src/infrastructure/bootstrap/bundle/images.lock")
	if err != nil {
		t.Fatalf("locate images.lock runfile: %v", err)
	}
	return path
}
