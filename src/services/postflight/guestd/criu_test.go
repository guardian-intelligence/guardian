package guestd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCRIUDumpAndRestoreContract(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "criu")
	script := `#!/bin/sh
set -eu
case "$1" in
  --version) echo 'Version: 4.2' ;;
  check) echo 'Looks good.' ;;
  dump)
    while [ "$#" -gt 0 ]; do
      [ "$1" = --images-dir ] && { shift; images="$1"; }
      shift
    done
    printf image >"$images/inventory.img"
    ;;
  restore)
    while [ "$#" -gt 0 ]; do
      [ "$1" = --pidfile ] && { shift; pidfile="$1"; }
      shift
    done
    printf '4321\n' >"$pidfile"
    ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	imagesRoot := filepath.Join(root, "encrypted")
	images := filepath.Join(imagesRoot, "generation-1")
	engine := CRIU{Path: bin, ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work")}
	capsule := Capsule{RootPID: 123, ImagesDir: images, ExternalMounts: []ExternalMount{{Key: "workspace", Path: "/workspace"}}}
	artifact, err := engine.Dump(context.Background(), capsule)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Version != "Version: 4.2" || !strings.HasPrefix(artifact.Digest, "sha256:") {
		t.Fatalf("artifact = %+v", artifact)
	}
	pid, err := engine.Restore(context.Background(), capsule, artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 4321 {
		t.Fatalf("pid = %d", pid)
	}
	if _, err := engine.Restore(context.Background(), capsule, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("tampered artifact restored")
	}
}

func TestCRIURejectsUnsafeCapsules(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	engine := CRIU{Path: "/usr/sbin/criu", ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work")}
	for name, capsule := range map[string]Capsule{
		"init":           {RootPID: 1, ImagesDir: filepath.Join(root, "encrypted", "g")},
		"plaintext path": {RootPID: 2, ImagesDir: filepath.Join(root, "elsewhere")},
		"relative mount": {RootPID: 2, ImagesDir: filepath.Join(root, "encrypted", "g"), ExternalMounts: []ExternalMount{{Key: "ws", Path: "relative"}}},
		"injected key":   {RootPID: 2, ImagesDir: filepath.Join(root, "encrypted", "g"), ExternalMounts: []ExternalMount{{Key: "ws]:bad", Path: "/workspace"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := engine.Dump(context.Background(), capsule); err == nil {
				t.Fatal("unsafe capsule accepted: " + strconv.Itoa(capsule.RootPID))
			}
		})
	}
}

func TestCRIURejectsPlaintextDiagnostics(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	engine := CRIU{
		Path:       "/usr/sbin/criu",
		ImagesRoot: imagesRoot,
		WorkRoot:   filepath.Join(root, "plaintext-work"),
	}
	_, err := engine.Dump(context.Background(), Capsule{
		RootPID:   2,
		ImagesDir: filepath.Join(imagesRoot, "images"),
	})
	if err == nil || !strings.Contains(err.Error(), "diagnostics must be inside") {
		t.Fatalf("plaintext diagnostics error = %v", err)
	}
}
