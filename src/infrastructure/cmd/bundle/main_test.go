package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"sigs.k8s.io/yaml"

	"github.com/guardian-intelligence/guardian/src/infrastructure/imageset"
)

const fixtureLock = `# comment line
ghcr.io/example/app:v1.0.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

ghcr.io/example/chart:0.1.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb # trailing comment
`

// The golden shape is what the pinned hauler consumes; the store-sync smoke
// in the drill pipeline is the live schema-compatibility check. Field names
// verified against pkg/apis/hauler.cattle.io/v1 at the pinned commit.
func TestHaulerManifestGolden(t *testing.T) {
	refs, err := imageset.ParseLock([]byte(fixtureLock))
	if err != nil {
		t.Fatalf("imageset.ParseLock() error = %v", err)
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
  - exclude-extras: true
    name: ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  - exclude-extras: true
    name: ghcr.io/example/app:v1.0.0
  - exclude-extras: true
    name: ghcr.io/example/chart@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  - exclude-extras: true
    name: ghcr.io/example/chart:0.1.0
`
	if string(payload) != golden {
		t.Fatalf("haulerManifest() =\n%s\nwant\n%s", payload, golden)
	}
}

func TestHaulerManifestDeterministic(t *testing.T) {
	refs, _ := imageset.ParseLock([]byte(fixtureLock))
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

// The production declared lock must project cleanly: every ref pinned,
// count preserved, order preserved. The generated union lock obeys the same
// invariants (enforced by //src/infrastructure/imageset and its Tier-1
// tests), so a lock this tool accepts is a lock the union derivation built.
func TestProductionImagesLockProjects(t *testing.T) {
	path, err := runfiles.Rlocation("_main/src/infrastructure/bootstrap/bundle/images.declared.lock")
	if err != nil {
		t.Fatalf("locate images.declared.lock runfile: %v", err)
	}
	lock, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read production declared lock: %v", err)
	}
	refs, err := imageset.ParseLock(lock)
	if err != nil {
		t.Fatalf("imageset.ParseLock(production lock) error = %v", err)
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
	// Every ref yields at least its digest entry; tagged refs (the Talos
	// system section) yield an extra tag entry, so images >= refs.
	if len(parsed.Spec.Images) < len(refs) {
		t.Fatalf("production manifest has %d images, want at least %d", len(parsed.Spec.Images), len(refs))
	}
}

func TestDescribeBundle(t *testing.T) {
	dir := t.TempDir()
	haulPath := dir + "/haul.tar.zst"
	if err := os.WriteFile(haulPath, []byte("haul-bytes"), 0o644); err != nil {
		t.Fatalf("write haul fixture: %v", err)
	}
	lock := []byte(fixtureLock)
	refs, err := imageset.ParseLock(lock)
	if err != nil {
		t.Fatalf("imageset.ParseLock() error = %v", err)
	}
	payload, err := describeBundle(lock, refs, haulPath, "abc123")
	if err != nil {
		t.Fatalf("describeBundle() error = %v", err)
	}
	var parsed bundleManifest
	if err := yaml.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("bundle manifest does not unmarshal: %v", err)
	}
	if parsed.Revision != "abc123" || parsed.Refs != 2 || parsed.HaulPath != "haul.tar.zst" {
		t.Fatalf("bundle manifest = %+v, want revision abc123, 2 refs, basename haul path", parsed)
	}
	if len(parsed.HaulSHA256) != 64 || len(parsed.ImagesLockSHA256) != 64 {
		t.Fatalf("bundle manifest digests malformed: %+v", parsed)
	}
	second, err := describeBundle(lock, refs, haulPath, "abc123")
	if err != nil {
		t.Fatalf("describeBundle() second call error = %v", err)
	}
	if string(payload) != string(second) {
		t.Fatalf("describeBundle() output is not deterministic")
	}
}

// writeBundleDir lays out a bundle directory exactly as `aspect infra
// bundle` leaves it — haul.tar.zst, hauler-manifest.yaml, and a
// bundle-manifest.yaml derived from the same lock and haul bytes — so
// verifyBundle exercises the real describeBundle/haulerManifest bindings.
func writeBundleDir(t *testing.T, lock []byte, haul []byte, revision string) (dir string, refs []string) {
	t.Helper()
	dir = t.TempDir()
	refs, err := imageset.ParseLock(lock)
	if err != nil {
		t.Fatalf("imageset.ParseLock() error = %v", err)
	}
	haulPath := dir + "/haul.tar.zst"
	if err := os.WriteFile(haulPath, haul, 0o644); err != nil {
		t.Fatalf("write haul fixture: %v", err)
	}
	haulerPayload, err := haulerManifest(refs)
	if err != nil {
		t.Fatalf("haulerManifest() error = %v", err)
	}
	if err := os.WriteFile(dir+"/hauler-manifest.yaml", haulerPayload, 0o644); err != nil {
		t.Fatalf("write hauler manifest fixture: %v", err)
	}
	manifest, err := describeBundle(lock, refs, haulPath, revision)
	if err != nil {
		t.Fatalf("describeBundle() error = %v", err)
	}
	if err := os.WriteFile(dir+"/bundle-manifest.yaml", manifest, 0o644); err != nil {
		t.Fatalf("write bundle manifest fixture: %v", err)
	}
	return dir, refs
}

func TestVerifyBundle(t *testing.T) {
	lock := []byte(fixtureLock)
	dir, refs := writeBundleDir(t, lock, []byte("haul-bytes"), "abc123")
	lines, err := verifyBundle(dir, lock, refs, "abc123")
	if err != nil {
		t.Fatalf("verifyBundle() error = %v", err)
	}
	// One line per binding: lock hash, ref count, haul hash, hauler
	// coverage, revision.
	if len(lines) != 5 {
		t.Fatalf("verifyBundle() = %d lines, want 5:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "verified: ") {
			t.Fatalf("verifyBundle() line %q is not a verified binding", line)
		}
	}
}

func TestVerifyBundleRevisionReportOnly(t *testing.T) {
	lock := []byte(fixtureLock)
	dir, refs := writeBundleDir(t, lock, []byte("haul-bytes"), "abc123")
	lines, err := verifyBundle(dir, lock, refs, "")
	if err != nil {
		t.Fatalf("verifyBundle() without --revision error = %v", err)
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "recorded: revision=abc123") {
		t.Fatalf("verifyBundle() revision line = %q, want report-only recorded revision", last)
	}
}

func TestVerifyBundleRevisionMismatch(t *testing.T) {
	lock := []byte(fixtureLock)
	dir, refs := writeBundleDir(t, lock, []byte("haul-bytes"), "abc123")
	_, err := verifyBundle(dir, lock, refs, "def456")
	if err == nil {
		t.Fatalf("verifyBundle() accepted a revision mismatch")
	}
	if !strings.Contains(err.Error(), "revision binding failed") {
		t.Fatalf("verifyBundle() error = %v, want revision binding detail", err)
	}
}

func TestVerifyBundleTamperedHaul(t *testing.T) {
	lock := []byte(fixtureLock)
	dir, refs := writeBundleDir(t, lock, []byte("haul-bytes"), "abc123")
	if err := os.WriteFile(dir+"/haul.tar.zst", []byte("tampered-haul"), 0o644); err != nil {
		t.Fatalf("tamper haul fixture: %v", err)
	}
	_, err := verifyBundle(dir, lock, refs, "abc123")
	if err == nil {
		t.Fatalf("verifyBundle() accepted a tampered haul")
	}
	if !strings.Contains(err.Error(), "haul binding failed") {
		t.Fatalf("verifyBundle() error = %v, want haul binding detail", err)
	}
}

func TestVerifyBundleTamperedLock(t *testing.T) {
	lock := []byte(fixtureLock)
	dir, _ := writeBundleDir(t, lock, []byte("haul-bytes"), "abc123")
	tampered := []byte("ghcr.io/evil/app@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\n")
	refs, err := imageset.ParseLock(tampered)
	if err != nil {
		t.Fatalf("imageset.ParseLock(tampered) error = %v", err)
	}
	_, err = verifyBundle(dir, tampered, refs, "abc123")
	if err == nil {
		t.Fatalf("verifyBundle() accepted a tampered lock")
	}
	if !strings.Contains(err.Error(), "images.lock binding failed") {
		t.Fatalf("verifyBundle() error = %v, want images.lock binding detail", err)
	}
}

func TestVerifyBundleMissingManifest(t *testing.T) {
	dir := t.TempDir()
	lock := []byte(fixtureLock)
	refs, err := imageset.ParseLock(lock)
	if err != nil {
		t.Fatalf("imageset.ParseLock() error = %v", err)
	}
	if _, err := verifyBundle(dir, lock, refs, "abc123"); err == nil {
		t.Fatalf("verifyBundle() accepted a bundle dir with no bundle-manifest.yaml")
	}
}

// A lock ref absent from the hauler manifest means the haul was built from a
// projection that never saw that ref — verify must fail even when the
// recorded lock hash matches, because the manifest binds the lock bytes, not
// the projection's coverage.
func TestVerifyBundleHaulerManifestMissingRef(t *testing.T) {
	lock := []byte(fixtureLock)
	dir, refs := writeBundleDir(t, lock, []byte("haul-bytes"), "abc123")
	partial, err := haulerManifest(refs[:1])
	if err != nil {
		t.Fatalf("haulerManifest() error = %v", err)
	}
	if err := os.WriteFile(dir+"/hauler-manifest.yaml", partial, 0o644); err != nil {
		t.Fatalf("write partial hauler manifest: %v", err)
	}
	// The bundle manifest still matches the lock and haul, so only the
	// hauler coverage binding differs.
	_, err = verifyBundle(dir, lock, refs, "abc123")
	if err == nil {
		t.Fatalf("verifyBundle() accepted a hauler manifest missing a lock ref")
	}
	if !strings.Contains(err.Error(), "hauler manifest binding failed") {
		t.Fatalf("verifyBundle() error = %v, want hauler manifest binding detail", err)
	}
}

// An entry in the hauler manifest that derives from no lock ref means the
// haul carries content the lock never declared — the entry sets must be
// EQUAL, not merely lock-covering, so verify must fail and name the extra.
func TestVerifyBundleHaulerManifestExtraRef(t *testing.T) {
	lock := []byte(fixtureLock)
	dir, refs := writeBundleDir(t, lock, []byte("haul-bytes"), "abc123")
	extra := "ghcr.io/evil/extra@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	padded, err := haulerManifest(append(append([]string{}, refs...), extra))
	if err != nil {
		t.Fatalf("haulerManifest() error = %v", err)
	}
	if err := os.WriteFile(dir+"/hauler-manifest.yaml", padded, 0o644); err != nil {
		t.Fatalf("write padded hauler manifest: %v", err)
	}
	_, err = verifyBundle(dir, lock, refs, "abc123")
	if err == nil {
		t.Fatalf("verifyBundle() accepted a hauler manifest with an entry not derived from the lock")
	}
	if !strings.Contains(err.Error(), "hauler manifest binding failed") || !strings.Contains(err.Error(), extra) {
		t.Fatalf("verifyBundle() error = %v, want hauler manifest binding detail naming %s", err, extra)
	}
}

func TestFilterStoredRefs(t *testing.T) {
	dir := t.TempDir()
	index := dir + "/index.json"
	if err := os.WriteFile(index, []byte(`{"manifests":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`), 0o644); err != nil {
		t.Fatalf("write index fixture: %v", err)
	}
	refs, err := imageset.ParseLock([]byte(fixtureLock))
	if err != nil {
		t.Fatalf("imageset.ParseLock() error = %v", err)
	}
	missing, err := filterStoredRefs(refs, index)
	if err != nil {
		t.Fatalf("filterStoredRefs() error = %v", err)
	}
	if len(missing) != 1 || missing[0] != refs[1] {
		t.Fatalf("filterStoredRefs() = %v, want only the chart ref", missing)
	}
}

func TestFilterStoredRefsFullyStored(t *testing.T) {
	dir := t.TempDir()
	index := dir + "/index.json"
	if err := os.WriteFile(index, []byte(`{"manifests":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`), 0o644); err != nil {
		t.Fatalf("write index fixture: %v", err)
	}
	refs, _ := imageset.ParseLock([]byte(fixtureLock))
	missing, err := filterStoredRefs(refs, index)
	if err != nil {
		t.Fatalf("filterStoredRefs() error = %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("filterStoredRefs() = %v, want empty (fully-synced store resumes to zero work)", missing)
	}
}

func TestHaulerRefFormsTagged(t *testing.T) {
	digestForm, tagForm := haulerRefForms("registry.k8s.io/pause:3.10.1@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c")
	if digestForm != "registry.k8s.io/pause@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c" {
		t.Fatalf("digestForm = %q", digestForm)
	}
	if tagForm != "registry.k8s.io/pause:3.10.1" {
		t.Fatalf("tagForm = %q, want the tag ref so the mirror answers tag pulls", tagForm)
	}
}

func TestHaulerRefFormsDigestOnly(t *testing.T) {
	digestForm, tagForm := haulerRefForms("ghcr.io/x/y@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if digestForm != "ghcr.io/x/y@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("digestForm = %q", digestForm)
	}
	if tagForm != "" {
		t.Fatalf("tagForm = %q, want empty for a digest-only ref", tagForm)
	}
}

// A tagged lock ref must project to both a digest entry and a tag entry so a
// dark mirror answers Talos's tag-addressed system-image pulls.
func TestHaulerManifestExpandsTaggedRefs(t *testing.T) {
	payload, err := haulerManifest([]string{"registry.k8s.io/pause:3.10.1@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c"})
	if err != nil {
		t.Fatalf("haulerManifest() error = %v", err)
	}
	var parsed haulerImages
	if err := yaml.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	names := map[string]bool{}
	for _, img := range parsed.Spec.Images {
		names[img.Name] = true
		if !img.ExcludeExtras {
			t.Fatalf("image %q missing exclude-extras", img.Name)
		}
	}
	for _, want := range []string{
		"registry.k8s.io/pause@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c",
		"registry.k8s.io/pause:3.10.1",
	} {
		if !names[want] {
			t.Fatalf("manifest missing %q; have %v", want, names)
		}
	}
}
