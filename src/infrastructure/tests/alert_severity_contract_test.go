package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Two severity vocabularies coexist and neither is validated before the
// cluster sees it. Alerta rejects a webhook whose severity is outside its
// accepted set with an HTTP 500 — Alertmanager then retries forever, the
// alert pages via the relay but never lands in Alerta, and the retry loop
// trips AlertmanagerNotificationsFailing the first time the mislabeled rule
// fires. Flagger's Canary CRD instead enumerates info|warn|error, and a
// value from the OTHER vocabulary fails the server dry-run and wedges the
// Kustomization (2026-07-25: "warning" on the iam canary blocked
// guardian-iam-prod and every dependent). Both contracts, one walk.
func TestAlertSeveritiesMatchTheirConsumers(t *testing.T) {
	alerta := map[string]bool{
		"security": true, "critical": true, "major": true, "minor": true,
		"warning": true, "indeterminate": true, "informational": true,
		"normal": true, "ok": true, "cleared": true, "debug": true,
		"trace": true, "unknown": true,
	}
	flagger := map[string]bool{"info": true, "warn": true, "error": true}
	severityLine := regexp.MustCompile(`(?m)^\s*severity:\s*["']?([A-Za-z_-]+)["']?\s*$`)

	files, vmruleLabels, canaryAlerts := 0, 0, 0
	checkVMRuleDoc := func(path string, doc map[string]interface{}) {
		raw := yamlSubtree(doc, "spec")
		for _, m := range severityLine.FindAllStringSubmatch(raw, -1) {
			vmruleLabels++
			if !alerta[m[1]] {
				t.Errorf("%s: VMRule severity %q is outside Alerta's accepted set (warning, critical, ...)", path, m[1])
			}
		}
	}
	checkCanaryDoc := func(path string, doc map[string]interface{}) {
		raw := yamlSubtree(doc, "spec")
		for _, m := range severityLine.FindAllStringSubmatch(raw, -1) {
			canaryAlerts++
			if !flagger[m[1]] {
				t.Errorf("%s: Flagger Canary alert severity %q is outside info|warn|error and fails the CRD dry-run", path, m[1])
			}
		}
	}
	checkConfigMapDoc := func(path string, doc map[string]interface{}) {
		data, _ := doc["data"].(map[string]interface{})
		for _, v := range data {
			blob, ok := v.(string)
			if !ok || !strings.Contains(blob, "alert:") {
				continue
			}
			for _, m := range severityLine.FindAllStringSubmatch(blob, -1) {
				vmruleLabels++
				if !alerta[m[1]] {
					t.Errorf("%s: embedded alert rule severity %q is outside Alerta's accepted set", path, m[1])
				}
			}
		}
	}

	// Runfiles materialize some directories as symlinks, which
	// filepath.WalkDir will not descend into — stat through them so coverage
	// does not depend on bazel's materialization strategy.
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
			for _, doc := range mapDocsLenient(t, path) {
				switch doc["kind"] {
				case "VMRule":
					checkVMRuleDoc(path, doc)
				case "Canary":
					checkCanaryDoc(path, doc)
				case "ConfigMap":
					checkConfigMapDoc(path, doc)
				}
			}
		}
	}
	walk(runfilePath("src/infrastructure"))

	// A silently empty walk (runfiles symlink quirks, moved roots, kind
	// renames) would make both contracts pass while checking nothing.
	if files < 50 || vmruleLabels < 50 || canaryAlerts < 1 {
		t.Fatalf("walk saw %d yaml files / %d VMRule severities / %d Canary severities — coverage collapsed", files, vmruleLabels, canaryAlerts)
	}
}

// mapDocsLenient decodes every YAML document in path and keeps the mapping
// docs; files that are top-level sequences (file_sd target lists) carry no
// alert rules and are skipped rather than fatal.
func mapDocsLenient(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var docs []map[string]interface{}
	dec := yaml.NewDecoder(strings.NewReader(string(payload)))
	for {
		var doc interface{}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if m, ok := doc.(map[string]interface{}); ok {
			docs = append(docs, m)
		}
	}
	return docs
}

// yamlSubtree re-serializes one top-level section of a parsed doc so the
// severity regex runs over rule labels without matching unrelated fields
// elsewhere in the file.
func yamlSubtree(doc map[string]interface{}, key string) string {
	section, ok := doc[key]
	if !ok {
		return ""
	}
	out, err := yaml.Marshal(section)
	if err != nil {
		return ""
	}
	return string(out)
}
