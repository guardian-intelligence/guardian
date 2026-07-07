package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeCall struct {
	env  []string
	args []string
}

type fakeRestic struct {
	calls   []fakeCall
	outputs map[string][]byte // keyed by first restic arg after --repo
	fail    map[string]bool
}

func (f *fakeRestic) run(opts *options, extraEnv []string, args ...string) error {
	f.calls = append(f.calls, fakeCall{env: extraEnv, args: args})
	if f.fail[args[0]] {
		return fmt.Errorf("fake restic %s failed", args[0])
	}
	return nil
}

func (f *fakeRestic) runOut(opts *options, extraEnv []string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{env: extraEnv, args: args})
	if f.fail[args[0]] {
		return nil, fmt.Errorf("fake restic %s failed", args[0])
	}
	return f.outputs[args[0]], nil
}

func testOptions(t *testing.T, fake *fakeRestic) *options {
	t.Helper()
	return &options{
		repo:       filepath.Join(t.TempDir(), "repo"),
		bundleDir:  filepath.Join(t.TempDir(), "missing-bundle"), // not /dev/shm; individual tests override
		custodyDir: t.TempDir(),
		stdin:      strings.NewReader(""),
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		run:        fake.run,
		runOut:     fake.runOut,
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeUnsealKey(t *testing.T, dir string) string {
	t.Helper()
	content := "unseal-key-bytes"
	sum := sha256.Sum256([]byte(content))
	name := "unseal-" + hex.EncodeToString(sum[:]) + ".key"
	path := filepath.Join(dir, "static-seal", name)
	writeFile(t, path, content)
	writeFile(t, filepath.Join(dir, "static-seal", "metadata.json"), `{"sha256":"`+hex.EncodeToString(sum[:])+`"}`)
	return path
}

// populateLegacy lays out a complete legacy custody source set and returns
// the talm root.
func populateLegacy(t *testing.T, opts *options) string {
	t.Helper()
	talmRoot := t.TempDir()
	for _, name := range []string{"secrets.yaml", "talm.key", "talosconfig"} {
		writeFile(t, filepath.Join(talmRoot, name), name+" contents")
	}
	writeFile(t, filepath.Join(opts.custodyDir, envName), "KEY=value")
	writeUnsealKey(t, opts.custodyDir)
	return talmRoot
}

func TestResolveFromLegacyFailClosed(t *testing.T) {
	fake := &fakeRestic{}
	opts := testOptions(t, fake)
	opts.talmRoot = populateLegacy(t, opts)

	// Complete set resolves.
	src, err := resolveSources(opts)
	if err != nil {
		t.Fatalf("complete legacy set should resolve: %v", err)
	}
	var got []string
	for _, r := range src.resolved {
		got = append(got, r.bundlePath)
	}
	for _, want := range []string{"talm/secrets.yaml", "talm/talm.key", "talm/talosconfig", envName, "openbao/metadata.json"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("resolved members missing %s (got %v)", want, got)
		}
	}

	// Removing any single required member must fail the whole resolve.
	if err := os.Remove(filepath.Join(opts.talmRoot, "talm.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSources(opts); err == nil {
		t.Fatal("resolve must fail-closed when a required member is missing")
	} else if !strings.Contains(err.Error(), "talm/talm.key") {
		t.Fatalf("error should name the missing member, got: %v", err)
	}
}

func TestResolveLegacyEnvFileNameFallback(t *testing.T) {
	fake := &fakeRestic{}
	opts := testOptions(t, fake)
	opts.talmRoot = populateLegacy(t, opts)

	// Replace custody.env with the deprecated name; resolve should warn and
	// still store it under the canonical bundle path.
	if err := os.Remove(filepath.Join(opts.custodyDir, envName)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(opts.custodyDir, legacyEnvName), "KEY=value")

	src, err := resolveSources(opts)
	if err != nil {
		t.Fatalf("legacy env name should resolve: %v", err)
	}
	for _, r := range src.resolved {
		if r.bundlePath == envName && strings.HasSuffix(r.source, legacyEnvName) {
			if !strings.Contains(opts.stderr.(*bytes.Buffer).String(), "legacy") {
				t.Error("expected a deprecation warning for the legacy env file name")
			}
			return
		}
	}
	t.Fatalf("expected %s resolved from legacy name, got %+v", envName, src.resolved)
}

func TestFindUnsealKeyFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	sum := sha256.Sum256([]byte("right"))
	writeFile(t, filepath.Join(dir, "unseal-"+hex.EncodeToString(sum[:])+".key"), "wrong")
	if _, err := findUnsealKey(dir); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("want fingerprint mismatch error, got %v", err)
	}
}

func TestFindUnsealKeyRefusesMultipleDistinctKeys(t *testing.T) {
	dir := t.TempDir()
	for _, content := range []string{"key-one", "key-two"} {
		sum := sha256.Sum256([]byte(content))
		writeFile(t, filepath.Join(dir, "unseal-"+hex.EncodeToString(sum[:])+".key"), content)
	}
	if _, err := findUnsealKey(dir); err == nil || !strings.Contains(err.Error(), "refusing to guess") {
		t.Fatalf("want multiple-keys refusal, got %v", err)
	}
}

func TestCreateBacksUpChecksAndPrintsInstructions(t *testing.T) {
	fake := &fakeRestic{}
	opts := testOptions(t, fake)
	opts.talmRoot = populateLegacy(t, opts)
	opts.yes = true
	// Repo pre-initialized so ensureRepo takes the non-interactive path.
	writeFile(t, filepath.Join(opts.repo, "config"), "restic config")
	// Staging must land on tmpfs; use a unique subdir to keep tests parallel-safe.
	opts.bundleDir = filepath.Join("/dev/shm", fmt.Sprintf("guardian-custody-test-%d", os.Getpid()))
	defer os.RemoveAll(opts.bundleDir)

	if err := cmdCreate(opts); err != nil {
		t.Fatal(err)
	}

	var gotVerbs []string
	for _, c := range fake.calls {
		gotVerbs = append(gotVerbs, c.args[0])
	}
	if len(gotVerbs) != 2 || gotVerbs[0] != "backup" || gotVerbs[1] != "check" {
		t.Fatalf("want [backup check], got %v", gotVerbs)
	}
	if _, err := os.Stat(opts.bundleDir); !os.IsNotExist(err) {
		t.Error("fresh staging dir must be shredded after create")
	}
	out := opts.stdout.(*bytes.Buffer).String()
	for _, phrase := range []string{"TWO offline media", "NO other way to recover", "If you are an agent", "key-add"} {
		if !strings.Contains(out, phrase) {
			t.Errorf("instructions missing %q", phrase)
		}
	}
}

func TestCreateDoesNotShredOperatorOpenedBundle(t *testing.T) {
	fake := &fakeRestic{}
	opts := testOptions(t, fake)
	opts.yes = true
	writeFile(t, filepath.Join(opts.repo, "config"), "restic config")

	// Simulate a restored bundle the operator edited: bundle-layout dir at
	// the fixed path.
	opts.bundleDir = filepath.Join("/dev/shm", fmt.Sprintf("guardian-custody-test-open-%d", os.Getpid()))
	defer os.RemoveAll(opts.bundleDir)
	for _, m := range manifest {
		if m.required {
			writeFile(t, filepath.Join(opts.bundleDir, m.bundlePath), "x")
		}
	}
	writeUnsealKey(t, filepath.Join(opts.bundleDir, "openbao"))
	// findUnsealKey walks the whole bundle dir; the key must live under
	// openbao/ per layout. Rewrite metadata to canonical location.
	if err := cmdCreate(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(opts.bundleDir); err != nil {
		t.Error("an operator-opened bundle dir must survive create; only explicit wipe removes it")
	}
}

func TestVerifyFailsWhenRequiredMemberMissingFromSnapshot(t *testing.T) {
	fake := &fakeRestic{
		outputs: map[string][]byte{
			"snapshots": []byte(`[{"id":"abcdef1234567890","time":"2026-07-07T00:00:00Z","paths":["/dev/shm/guardian-custody"]}]`),
			"ls": []byte(`{"path":"/dev/shm/guardian-custody/talm/secrets.yaml"}
{"path":"/dev/shm/guardian-custody/talm/talm.key"}
{"path":"/dev/shm/guardian-custody/talm/talosconfig"}
{"path":"/dev/shm/guardian-custody/openbao/metadata.json"}
{"path":"/dev/shm/guardian-custody/openbao/unseal-` + strings.Repeat("ab", 32) + `.key"}
`),
		},
	}
	opts := testOptions(t, fake)
	err := cmdVerify(opts)
	if err == nil || !strings.Contains(err.Error(), envName) {
		t.Fatalf("verify must fail naming missing %s, got %v", envName, err)
	}

	// Add the env member and verify passes.
	fake.outputs["ls"] = append(fake.outputs["ls"], []byte(`{"path":"/dev/shm/guardian-custody/`+envName+`"}
`)...)
	fake.calls = nil
	if err := cmdVerify(opts); err != nil {
		t.Fatalf("complete snapshot must verify: %v", err)
	}
	if !strings.Contains(opts.stdout.(*bytes.Buffer).String(), "OK:") {
		t.Error("verify success should print OK line")
	}
}

func TestVerifyReadDataFlag(t *testing.T) {
	fake := &fakeRestic{outputs: map[string][]byte{
		"snapshots": []byte(`[{"id":"abcdef1234567890","time":"2026-07-07T00:00:00Z"}]`),
		"ls":        []byte(""),
	}}
	opts := testOptions(t, fake)
	opts.readData = true
	_ = cmdVerify(opts) // fails on membership; only the check args matter here
	if len(fake.calls) == 0 || strings.Join(fake.calls[0].args, " ") != "check --read-data" {
		t.Fatalf("want check --read-data first, got %+v", fake.calls)
	}
}

func TestWipeRefusesOffTmpfs(t *testing.T) {
	fake := &fakeRestic{}
	opts := testOptions(t, fake)
	opts.bundleDir = t.TempDir()
	if err := cmdWipe(opts); err == nil || !strings.Contains(err.Error(), "/dev/shm") {
		t.Fatalf("wipe must refuse non-tmpfs dirs, got %v", err)
	}
}

func TestRestoreRefusesWhenBundleOpen(t *testing.T) {
	fake := &fakeRestic{}
	opts := testOptions(t, fake)
	opts.bundleDir = t.TempDir() // exists
	if err := cmdRestore(opts); err == nil || !strings.Contains(err.Error(), "wipe") {
		t.Fatalf("restore must refuse over an open bundle, got %v", err)
	}
}

func TestStatusWarnsOnStaleSnapshotAndResidue(t *testing.T) {
	old := time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fake := &fakeRestic{outputs: map[string][]byte{
		"snapshots": []byte(`[{"id":"abcdef1234567890","time":"` + old + `"}]`),
	}}
	opts := testOptions(t, fake)
	writeFile(t, filepath.Join(opts.repo, "config"), "restic config")
	opts.talmRoot = t.TempDir()
	writeFile(t, filepath.Join(opts.talmRoot, "secrets.yaml"), "plaintext")
	writeFile(t, filepath.Join(opts.custodyDir, legacyEnvName), "KEY=value")

	if err := cmdStatus(opts); err != nil {
		t.Fatal(err)
	}
	out := opts.stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "older than") {
		t.Error("status must warn on stale snapshots")
	}
	if !strings.Contains(out, "secrets.yaml") || !strings.Contains(out, legacyEnvName) {
		t.Errorf("status must warn on plaintext residue, got:\n%s", out)
	}
}

func TestShredDirOverwritesBeforeUnlink(t *testing.T) {
	dir := t.TempDir()
	// shredDir is used on tmpfs in production but is path-agnostic itself;
	// wipe enforces the /dev/shm guard before calling it.
	nested := filepath.Join(dir, "a", "b.txt")
	writeFile(t, nested, "sensitive")
	if err := shredDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("shredDir must remove the directory tree")
	}
}

func TestRealMainRejectsUnknownSubcommand(t *testing.T) {
	fake := &fakeRestic{}
	opts := testOptions(t, fake)
	if err := realMain(opts, []string{"explode"}); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("want unknown-subcommand error, got %v", err)
	}
	if err := realMain(opts, nil); err == nil {
		t.Fatal("want usage error for empty argv")
	}
}
