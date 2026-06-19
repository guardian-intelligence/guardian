package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractTarGzMember(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "tool.tar.gz")
	if err := writeTestTarGz(archive, map[string]string{
		"README.md": "ignored",
		"oras":      "binary contents",
	}); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "bin", "oras")
	if err := extractTarGzMember(archive, "oras", dest); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "binary contents" {
		t.Fatalf("extracted = %q", raw)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestExtractTarGzMemberRequiresExistingRegularFile(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "tool.tar.gz")
	if err := writeTestTarGz(archive, map[string]string{"other": "payload"}); err != nil {
		t.Fatal(err)
	}

	err := extractTarGzMember(archive, "oras", filepath.Join(dir, "oras"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `archive member "oras" not found`) {
		t.Fatalf("error = %v", err)
	}
}

func writeTestTarGz(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	for name, contents := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(contents)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			return err
		}
	}
	return nil
}
