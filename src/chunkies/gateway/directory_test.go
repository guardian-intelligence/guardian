package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChunkDirectory(t *testing.T) {
	got, err := parseChunkDirectory(strings.NewReader(`
# the WUM fleet
wum park-mythra 127.0.0.1:9632

wum park-canary 127.0.0.1:9642
side pong-1 10.0.0.9:9632
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"wum/park-mythra": "127.0.0.1:9632",
		"wum/park-canary": "127.0.0.1:9642",
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
		"wum park-mythra",                      // missing addr
		"wum park-mythra 127.0.0.1:9632 huh",   // trailing field
		"wum=park-mythra=127.0.0.1:9632 x y z", // wrong count either way
	} {
		if _, err := parseChunkDirectory(strings.NewReader(bad)); err == nil {
			t.Fatalf("parse accepted %q", bad)
		}
	}
}

func TestLoadChunkDirectoryFailsClosed(t *testing.T) {
	d := newChunkDirectory()
	d.replace(map[string]string{"wum/park-old": "1.2.3.4:1"})

	if err := loadChunkDirectory(filepath.Join(t.TempDir(), "missing"), d); err == nil {
		t.Fatal("load of a missing file did not error")
	}
	if d.allowed("wum", "park-old") {
		t.Fatal("a failed load left stale chunks routable")
	}

	path := filepath.Join(t.TempDir(), "chunks.conf")
	os.WriteFile(path, []byte("wum park-a 127.0.0.1:1\nbroken line\n"), 0o600)
	if err := loadChunkDirectory(path, d); err == nil {
		t.Fatal("load of a malformed file did not error")
	}
	if d.allowed("wum", "park-a") {
		t.Fatal("a partially parsed file leaked entries")
	}
}
