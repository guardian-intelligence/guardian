package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"sigs.k8s.io/yaml"
)

const fixtureLock = `# comment line
ghcr.io/example/app:v1.0.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

ghcr.io/example/chart:0.1.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb # trailing comment
`

func TestParseImagesLock(t *testing.T) {
	refs, err := parseImagesLock([]byte(fixtureLock))
	if err != nil {
		t.Fatalf("parseImagesLock() error = %v", err)
	}
	want := []string{
		"ghcr.io/example/app:v1.0.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"ghcr.io/example/chart:0.1.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if len(refs) != len(want) {
		t.Fatalf("parseImagesLock() = %d refs, want %d", len(refs), len(want))
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("parseImagesLock()[%d] = %q, want %q", i, refs[i], want[i])
		}
	}
}

func TestParseImagesLockRejectsUnpinnedRef(t *testing.T) {
	_, err := parseImagesLock([]byte("ghcr.io/example/app:v1.0.0\n"))
	if err == nil {
		t.Fatalf("parseImagesLock() accepted a tag-only ref; this tool must never resolve tags")
	}
	if !strings.Contains(err.Error(), "not digest-pinned") {
		t.Fatalf("parseImagesLock() error = %v, want pin detail", err)
	}
}

func TestParseImagesLockRejectsEmptyLock(t *testing.T) {
	if _, err := parseImagesLock([]byte("# only comments\n")); err == nil {
		t.Fatalf("parseImagesLock() accepted an empty lock")
	}
}

// The golden shape is what the pinned hauler consumes; the store-sync smoke
// in the drill pipeline is the live schema-compatibility check. Field names
// verified against pkg/apis/hauler.cattle.io/v1 at the pinned commit.
func TestHaulerManifestGolden(t *testing.T) {
	refs, err := parseImagesLock([]byte(fixtureLock))
	if err != nil {
		t.Fatalf("parseImagesLock() error = %v", err)
	}
	payload, err := haulerManifest(refs)
	if err != nil {
		t.Fatalf("haulerManifest() error = %v", err)
	}
	golden := `apiVersion: content.hauler.cattle.io/v1
kind: Images
metadata:
  name: guardian-bundle-images
spec:
  images:
  - name: ghcr.io/example/app:v1.0.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  - name: ghcr.io/example/chart:0.1.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`
	if string(payload) != golden {
		t.Fatalf("haulerManifest() =\n%s\nwant\n%s", payload, golden)
	}
}

func TestHaulerManifestDeterministic(t *testing.T) {
	refs, _ := parseImagesLock([]byte(fixtureLock))
	first, err := haulerManifest(refs)
	if err != nil {
		t.Fatalf("haulerManifest() error = %v", err)
	}
	second, err := haulerManifest(refs)
	if err != nil {
		t.Fatalf("haulerManifest() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("haulerManifest() output is not deterministic")
	}
}

// The production lock must project cleanly: every ref pinned, count preserved,
// order preserved. This is the generator's contract with the Tier-1
// conformance test that keeps the lock complete.
func TestProductionImagesLockProjects(t *testing.T) {
	path, err := runfiles.Rlocation("_main/src/infrastructure/bootstrap/bundle/images.lock")
	if err != nil {
		t.Fatalf("locate images.lock runfile: %v", err)
	}
	lock, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read production images.lock: %v", err)
	}
	refs, err := parseImagesLock(lock)
	if err != nil {
		t.Fatalf("parseImagesLock(production lock) error = %v", err)
	}
	if len(refs) < 100 {
		t.Fatalf("production lock projected only %d refs; expected the full inventory (anti-vacuity floor 100)", len(refs))
	}
	payload, err := haulerManifest(refs)
	if err != nil {
		t.Fatalf("haulerManifest(production lock) error = %v", err)
	}
	var parsed haulerImages
	if err := yaml.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("production manifest does not unmarshal: %v", err)
	}
	if len(parsed.Spec.Images) != len(refs) {
		t.Fatalf("production manifest has %d images, want %d", len(parsed.Spec.Images), len(refs))
	}
}
