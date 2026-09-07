package guestd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSealPurgesAuthenticationAndActionStatePreservingBuildCache(t *testing.T) {
	home := t.TempDir()
	for _, relative := range []string{"_work/_temp/_runner_file_commands/save_state", ".docker/config.json", ".npmrc", ".git-credentials", ".netrc", ".config/gh/hosts.yml", ".cache/bazel/artifact"} {
		path := filepath.Join(home, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := PurgeRunnerEphemeral(home); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"_work/_temp", ".docker/config.json", ".npmrc", ".git-credentials", ".netrc", ".config/gh/hosts.yml"} {
		if _, err := os.Lstat(filepath.Join(home, relative)); !os.IsNotExist(err) {
			t.Fatalf("ephemeral %s survived: %v", relative, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".cache/bazel/artifact")); err != nil {
		t.Fatalf("build cache lost: %v", err)
	}
	if err := PurgeRunnerEphemeral(home); err != nil {
		t.Fatalf("cleanup not idempotent: %v", err)
	}
}
