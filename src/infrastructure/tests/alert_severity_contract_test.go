package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Alerta rejects any webhook whose severity is outside its accepted set with
// an HTTP 500, and Alertmanager then retries that notification forever — the
// alert pages via the relay but never lands in Alerta, and the retry loop
// trips AlertmanagerNotificationsFailing. The mistake only surfaces the first
// time the mislabeled alert actually fires, so the contract has to be
// enforced at commit time.
func TestAlertSeveritiesAreAcceptedByAlerta(t *testing.T) {
	accepted := map[string]bool{
		"security": true, "critical": true, "major": true, "minor": true,
		"warning": true, "indeterminate": true, "informational": true,
		"normal": true, "ok": true, "cleared": true, "debug": true,
		"trace": true, "unknown": true,
	}
	severityLine := regexp.MustCompile(`(?m)^\s*severity:\s*["']?([A-Za-z_-]+)["']?\s*$`)
	root := runfilePath("src/infrastructure")
	files, matches := 0, 0
	// Runfiles materialize some directories as symlinks, which
	// filepath.WalkDir will not descend into — stat through them instead so
	// coverage does not silently depend on bazel's materialization strategy.
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", path, err)
			}
			if info.IsDir() {
				walk(path)
				continue
			}
			if !strings.HasSuffix(path, ".yaml") {
				continue
			}
			files++
			raw := readText(t, path)
			for _, m := range severityLine.FindAllStringSubmatch(raw, -1) {
				matches++
				if !accepted[m[1]] {
					t.Errorf("%s labels an alert severity: %q, which Alerta rejects with a 500 (accepted: warning, critical, ...)", path, m[1])
				}
			}
		}
	}
	walk(root)
	// A silently empty walk (runfiles symlink quirks, moved roots) would make
	// this contract pass while checking nothing.
	if files < 50 || matches < 50 {
		t.Fatalf("walk saw %d yaml files / %d severity labels — coverage collapsed", files, matches)
	}
}
