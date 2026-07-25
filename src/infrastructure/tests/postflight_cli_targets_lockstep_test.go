package tests

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// One release target set has to reach every surface that hands a user a binary:
// the lane that builds and signs them, the cutter that attaches them to the
// release, the npm lane's target-to-platform map, the bin shim that dispatches
// on process.platform, the meta package's optionalDependencies, each platform
// package's own name and os/cpu constraints, and the formula's per-arch download
// URLs and checksum pins. Adding a target to the build lane alone is silent —
// nothing is red and nothing pages, and the gap reaches users as "no prebuilt
// binary for your platform" from a release that has one. This test is the only
// thing that makes them move together.
func TestPostflightCliReleaseTargetsMoveTogether(t *testing.T) {
	const imageWorkflow = ".github/workflows/postflight-cli-image.yml"

	targets := workflowTargetList(t, imageWorkflow, "TARGETS")
	if len(targets) == 0 {
		t.Fatalf("%s declares no release targets", imageWorkflow)
	}

	assertSameSet(t, "the release cutter attaches", targets, workflowTargetList(t, ".github/workflows/postflight-cli-release.yml", "TARGETS"))

	platforms := npmPlatformMap(t)
	packedTargets := make([]string, 0, len(platforms))
	for target := range platforms {
		packedTargets = append(packedTargets, target)
	}
	assertSameSet(t, "the npm lane packs", targets, packedTargets)

	packages := make([]string, 0, len(platforms))
	shimKeys := make([]string, 0, len(platforms))
	for _, suffix := range platforms {
		packages = append(packages, "@guardian-intelligence/postflight-"+suffix)
		shimKeys = append(shimKeys, strings.Replace(suffix, "-", " ", 1))
	}

	assertSameSet(t, "the npm shim dispatches on", shimKeys, shimPlatformKeys(t))
	assertSameSet(t, "the npm shim resolves", packages, shimPackages(t))
	assertSameSet(t, "the meta package depends on", packages, metaOptionalDependencies(t))

	assertFormulaCoversTargets(t, targets)

	for target, suffix := range platforms {
		assertPlatformPackageDeclaresItsPlatform(t, target, suffix)
	}
}

func workflowEnv(t *testing.T, path string) map[string]string {
	t.Helper()

	var workflow struct {
		Env map[string]string `yaml:"env"`
	}
	if err := yaml.Unmarshal([]byte(readText(t, runfilePath(path))), &workflow); err != nil {
		t.Fatalf("%s does not parse as YAML: %v", path, err)
	}
	return workflow.Env
}

func workflowTargetList(t *testing.T, path, key string) []string {
	t.Helper()

	value, ok := workflowEnv(t, path)[key]
	if !ok {
		t.Fatalf("%s declares no %s env; the release target set has to stay readable from here", path, key)
	}
	return strings.Fields(value)
}

// PLATFORMS carries `<rust target>:<npm platform suffix>` pairs.
func npmPlatformMap(t *testing.T) map[string]string {
	t.Helper()

	const path = ".github/workflows/postflight-cli-publish-npm.yml"
	pairs := workflowTargetList(t, path, "PLATFORMS")
	platforms := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		target, suffix, ok := strings.Cut(pair, ":")
		if !ok {
			t.Fatalf("%s PLATFORMS entry %q is not a <target>:<suffix> pair", path, pair)
		}
		platforms[target] = suffix
	}
	return platforms
}

func shimEntries(t *testing.T) [][]string {
	t.Helper()

	const path = "src/products/postflight-cli/dist/npm/postflight/bin/postflight"
	entries := regexp.MustCompile(`"([a-z0-9]+ [a-z0-9_]+)":\s*"(@guardian-intelligence/postflight-[a-z0-9-]+)"`).
		FindAllStringSubmatch(readText(t, runfilePath(path)), -1)
	if entries == nil {
		t.Fatalf("%s declares no PACKAGES entries", path)
	}
	return entries
}

func shimPlatformKeys(t *testing.T) []string {
	t.Helper()

	var keys []string
	for _, entry := range shimEntries(t) {
		keys = append(keys, entry[1])
	}
	return keys
}

func shimPackages(t *testing.T) []string {
	t.Helper()

	var packages []string
	for _, entry := range shimEntries(t) {
		packages = append(packages, entry[2])
	}
	return packages
}

func metaOptionalDependencies(t *testing.T) []string {
	t.Helper()

	const path = "src/products/postflight-cli/dist/npm/postflight/package.json"
	var manifest struct {
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal([]byte(readText(t, runfilePath(path))), &manifest); err != nil {
		t.Fatalf("%s does not parse as JSON: %v", path, err)
	}

	packages := make([]string, 0, len(manifest.OptionalDependencies))
	for name := range manifest.OptionalDependencies {
		packages = append(packages, name)
	}
	return packages
}

func assertPlatformPackageDeclaresItsPlatform(t *testing.T, target, suffix string) {
	t.Helper()

	path := "src/products/postflight-cli/dist/npm/postflight-" + suffix + "/package.json"
	var manifest struct {
		Name string   `json:"name"`
		OS   []string `json:"os"`
		CPU  []string `json:"cpu"`
	}
	if err := json.Unmarshal([]byte(readText(t, runfilePath(path))), &manifest); err != nil {
		t.Fatalf("%s does not parse as JSON: %v", path, err)
	}

	os, cpu, _ := strings.Cut(suffix, "-")
	if want := "@guardian-intelligence/postflight-" + suffix; manifest.Name != want {
		t.Fatalf("%s is named %q but sits where npm resolves %q for release target %s", path, manifest.Name, want, target)
	}
	assertSameSet(t, path+" installs on", []string{os}, manifest.OS)
	assertSameSet(t, path+" installs on", []string{cpu}, manifest.CPU)
}

func assertFormulaCoversTargets(t *testing.T, targets []string) {
	t.Helper()

	const path = "src/products/postflight-cli/dist/homebrew/postflight.rb.tmpl"
	formula := readText(t, runfilePath(path))

	var downloaded, placeholders []string
	for _, match := range regexp.MustCompile(`/postflight-cli%2Fv@VERSION@/postflight-([A-Za-z0-9_.-]+)"`).FindAllStringSubmatch(formula, -1) {
		downloaded = append(downloaded, match[1])
	}
	for _, match := range regexp.MustCompile(`sha256 "@SHA256_([A-Z0-9_]+)@"`).FindAllStringSubmatch(formula, -1) {
		placeholders = append(placeholders, match[1])
	}

	expectedPlaceholders := make([]string, 0, len(targets))
	for _, target := range targets {
		expectedPlaceholders = append(expectedPlaceholders, strings.ToUpper(strings.ReplaceAll(target, "-", "_")))
	}

	assertSameSet(t, path+" downloads", targets, downloaded)
	assertSameSet(t, path+" pins checksums for", expectedPlaceholders, placeholders)
}

func assertSameSet(t *testing.T, context string, want, got []string) {
	t.Helper()

	wantSorted := append([]string(nil), want...)
	gotSorted := append([]string(nil), got...)
	sort.Strings(wantSorted)
	sort.Strings(gotSorted)

	if strings.Join(wantSorted, " ") != strings.Join(gotSorted, " ") {
		t.Fatalf("%s [%s]; the release target set is [%s]", context, strings.Join(gotSorted, " "), strings.Join(wantSorted, " "))
	}
}
