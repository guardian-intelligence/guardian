package tests

// Tier-1 image inventory conformance:
//   - images.declared.lock is well-formed (digest-pinned, no duplicate
//     (repo, digest) pairs)
//   - every artifact reference rendered from the manifest trees is
//     digest-pinned and registry-qualified
//   - declared and rendered are disjoint and their union derives cleanly —
//     the exact derivation CI signs on main and `aspect infra bundle`
//     re-runs, so a cold guardian-mgmt bootstrap can serve every artifact
//     from the workstation mirror.
//
// Extraction rules live in //src/infrastructure/imageset, shared with the
// imageset generator: a ref these tests see is a ref the tool sees, by
// construction.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/guardian-intelligence/guardian/src/infrastructure/imageset"
)

const declaredLockRunfile = "src/infrastructure/bootstrap/bundle/images.declared.lock"

func declaredLockEntries(t *testing.T) []string {
	t.Helper()
	refs, err := imageset.ParseLock([]byte(readText(t, runfilePath(declaredLockRunfile))))
	if err != nil {
		t.Fatalf("%s: %v", declaredLockRunfile, err)
	}
	return refs
}

// repoRootFromRunfiles derives the runfiles repo root so the shared
// extractor can walk the manifest trees exactly as the imageset CLI walks a
// checkout.
func repoRootFromRunfiles(t *testing.T) string {
	t.Helper()
	const anchor = "src/infrastructure/base/flux/sync.yaml"
	anchorPath := filepath.ToSlash(runfilePath(anchor))
	root := strings.TrimSuffix(anchorPath, anchor)
	if root == anchorPath {
		t.Fatalf("cannot derive runfiles repo root from %s", anchorPath)
	}
	return root
}

func renderedLockEntries(t *testing.T) []string {
	t.Helper()
	extracted, err := imageset.CollectRendered(repoRootFromRunfiles(t))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := imageset.Rendered(extracted)
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

// The library enforces well-formedness, digest pinning, registry
// qualification, and anti-vacuity in its own error paths; these tests
// exercise those paths against the real declared lock and manifest trees.
func TestDeclaredLockWellFormed(t *testing.T) {
	declaredLockEntries(t)
}

func TestRenderedImagesDigestPinned(t *testing.T) {
	renderedLockEntries(t)
}

// TestUnionLockDerives proves the full inventory derivation: disjointness
// between the declared and rendered halves, and a union that re-parses
// under the lock invariants.
func TestUnionLockDerives(t *testing.T) {
	declared := declaredLockEntries(t)
	rendered := renderedLockEntries(t)
	payload, err := imageset.UnionFile(declared, rendered)
	if err != nil {
		t.Fatal(err)
	}
	union, err := imageset.ParseLock(payload)
	if err != nil {
		t.Fatalf("generated union does not re-parse as a lock: %v", err)
	}
	if len(union) != len(declared)+len(rendered) {
		t.Fatalf("union has %d entries, want %d declared + %d rendered", len(union), len(declared), len(rendered))
	}
}
