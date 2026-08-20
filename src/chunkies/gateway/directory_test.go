package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChunkDirectory(t *testing.T) {
	got, err := parseChunkDirectory(strings.NewReader(`
# the toy fleet
toy chunk-main 127.0.0.1:9632

toy chunk-canary 127.0.0.1:9642
side pong-1 10.0.0.9:9632
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"toy/chunk-main": "127.0.0.1:9632",
		"toy/chunk-canary": "127.0.0.1:9642",
		"side/pong-1":     "10.0.0.9:9632",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("chunk %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseChunkDirectoryRejectsMalformedLines(t *testing.T) {
	for _, bad := range []string{
		"toy chunk-main",                      // missing addr
		"toy chunk-main 127.0.0.1:9632 huh",   // trailing field
		"toy=chunk-main=127.0.0.1:9632 x y z", // wrong count either way
	} {
		if _, err := parseChunkDirectory(strings.NewReader(bad)); err == nil {
			t.Fatalf("parse accepted %q", bad)
		}
	}
}

func TestLoadChunkDirectoryFailsClosed(t *testing.T) {
	d := newChunkDirectory()
	d.replace(map[string]string{"toy/chunk-old": "1.2.3.4:1"})

	if err := loadChunkDirectory(filepath.Join(t.TempDir(), "missing"), d); err == nil {
		t.Fatal("load of a missing file did not error")
	}
	if d.allowed("toy", "chunk-old") {
		t.Fatal("a failed load left stale chunks routable")
	}

	path := filepath.Join(t.TempDir(), "chunks.conf")
	os.WriteFile(path, []byte("toy chunk-a 127.0.0.1:1\nbroken line\n"), 0o600)
	if err := loadChunkDirectory(path, d); err == nil {
		t.Fatal("load of a malformed file did not error")
	}
	if d.allowed("toy", "chunk-a") {
		t.Fatal("a partially parsed file leaked entries")
	}
}
