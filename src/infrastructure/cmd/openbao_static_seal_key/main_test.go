package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunWritesStaticSealKeyArtifact(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 6, 28, 12, 34, 56, 0, time.UTC)
	keyBytes := bytes.Repeat([]byte{0x42}, staticSealKeyBytes)
	sum := sha256.Sum256(keyBytes)
	fingerprint := hex.EncodeToString(sum[:])
	var stdout bytes.Buffer

	err := run(options{
		cluster: "guardian-mgmt",
		region:  "ash",
		home:    home,
		nodeDir: defaultNodeDir,
		now:     now,
		random:  bytes.NewReader(keyBytes),
		stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	outDir := filepath.Join(home, ".guardian", "openbao", "guardian-mgmt-ash", "static-seal", fingerprint)
	keyPath := filepath.Join(outDir, "unseal-"+fingerprint+".key")
	metadataPath := filepath.Join(outDir, "metadata.json")

	assertMode(t, outDir, 0o700)
	assertMode(t, keyPath, 0o600)
	assertMode(t, metadataPath, 0o600)

	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if !bytes.Equal(gotKey, keyBytes) {
		t.Fatalf("unexpected key bytes")
	}

	var got metadata
	payload, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}

	if got.Bytes != staticSealKeyBytes {
		t.Fatalf("metadata bytes = %d, want %d", got.Bytes, staticSealKeyBytes)
	}
	if got.KeyID != fingerprint {
		t.Fatalf("metadata key id = %q, want %q", got.KeyID, fingerprint)
	}
	if got.SHA256 != fingerprint {
		t.Fatalf("metadata sha256 = %q, want %q", got.SHA256, fingerprint)
	}
	if got.OpenBAOFileURI != "file:///openbao/secrets/unseal-"+fingerprint+".key" {
		t.Fatalf("metadata OpenBAOFileURI = %q", got.OpenBAOFileURI)
	}
	if strings.Contains(stdout.String(), string(keyBytes)) {
		t.Fatalf("stdout leaked key bytes")
	}
}

func TestRunRefusesExistingArtifact(t *testing.T) {
	home := t.TempDir()
	opts := options{
		cluster: "guardian-mgmt",
		region:  "ash",
		home:    home,
		nodeDir: defaultNodeDir,
		now:     time.Date(2026, 6, 28, 12, 34, 56, 0, time.UTC),
		random:  bytes.NewReader(bytes.Repeat([]byte{0x11}, staticSealKeyBytes)),
		stdout:  &bytes.Buffer{},
	}
	if err := run(opts); err != nil {
		t.Fatalf("first run: %v", err)
	}

	opts.random = bytes.NewReader(bytes.Repeat([]byte{0x11}, staticSealKeyBytes))
	err := run(opts)
	if err == nil {
		t.Fatalf("second run unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite existing file") {
		t.Fatalf("second run error = %v", err)
	}
}

func TestRunRejectsUnsafeNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts options
	}{
		{
			name: "cluster path traversal",
			opts: options{cluster: "../guardian", region: "ash", keyID: "key1"},
		},
		{
			name: "explicit key id path traversal",
			opts: options{cluster: "guardian-mgmt", region: "ash", keyID: "../key"},
		},
		{
			name: "filename path traversal",
			opts: options{cluster: "guardian-mgmt", region: "ash", keyID: "key1", filename: "../key"},
		},
		{
			name: "relative node dir",
			opts: options{cluster: "guardian-mgmt", region: "ash", keyID: "key1", filename: "key", nodeDir: "var/lib/openbao"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.home = t.TempDir()
			tc.opts.now = time.Date(2026, 6, 28, 12, 34, 56, 0, time.UTC)
			tc.opts.random = bytes.NewReader(bytes.Repeat([]byte{0x11}, staticSealKeyBytes))
			tc.opts.stdout = &bytes.Buffer{}
			if err := run(tc.opts); err == nil {
				t.Fatalf("run unexpectedly succeeded")
			}
		})
	}
}

func TestRunRejectsExplicitKeyIDMismatch(t *testing.T) {
	err := run(options{
		cluster: "guardian-mgmt",
		region:  "ash",
		keyID:   "wrong-key-id",
		home:    t.TempDir(),
		nodeDir: defaultNodeDir,
		now:     time.Date(2026, 6, 28, 12, 34, 56, 0, time.UTC),
		random:  bytes.NewReader(bytes.Repeat([]byte{0x11}, staticSealKeyBytes)),
		stdout:  &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("run unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "key-id must match generated key SHA-256 fingerprint") {
		t.Fatalf("run error = %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %v, want %v", path, got, want)
	}
}
