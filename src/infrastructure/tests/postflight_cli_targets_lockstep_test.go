package tests

import (
	"encoding/json"
	"path/filepath"
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

// Two places prove a cross-compiled binary is the object its directory name
// claims by reading the same two header fields out of it: the edge lane, before
// it signs anything, and the deep-test runner, before a promotion can pick the
// artifact up. Neither can run three of the four targets, so the table of magic
// and machine words IS the check — a wrong offset on either side turns it into
// one that passes on anything, and a target added to TARGETS with no row of its
// own falls out of the check wherever the row is missing. Nothing at runtime
// compares the two tables. This does.
func TestPostflightCliObjectShapeTablesAgree(t *testing.T) {
	const (
		imageWorkflow  = ".github/workflows/postflight-cli-image.yml"
		deeptestRunner = "src/infrastructure/deployments/guardian/promotion/cli-deeptest-runner.yaml"
	)

	targets := workflowTargetList(t, imageWorkflow, "TARGETS")
	lane := objectShapeTable(t, imageWorkflow)
	runner := objectShapeTable(t, deeptestRunner)

	assertSameSet(t, imageWorkflow+" declares object shapes for", targets, shapedTargets(lane))
	assertSameSet(t, deeptestRunner+" declares object shapes for", targets, shapedTargets(runner))

	for _, target := range targets {
		if lane[target] != runner[target] {
			t.Errorf("%s reads %s as %s but %s reads it as %s; one target, one object shape",
				imageWorkflow, target, lane[target], deeptestRunner, runner[target])
		}
	}
}

// The install canary inspects the targets it cannot execute too, but by object
// class rather than per target, so its arms stay correct as targets are added
// within a class and go silent the moment one arrives from a class it has never
// seen: the `for` loop keeps iterating, `case` matches nothing, and the target
// is waved through with no check and no message.
func TestInstallCanaryStructuralCheckCoversEveryTarget(t *testing.T) {
	const canary = "src/infrastructure/deployments/guardian/promotion/cli-install-canary.yaml"

	targets := workflowTargetList(t, ".github/workflows/postflight-cli-image.yml", "TARGETS")
	assertSameSet(t, canary+" sweeps", targets, strings.Fields(containerEnvValue(t, canary, "TARGETS")))

	arms := targetCaseArms(t, canary)
	for _, target := range targets {
		matched := false
		for _, arm := range arms {
			if ok, err := filepath.Match(arm, target); err == nil && ok {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("%s checks the object format of [%s] and none of them matches %s",
				canary, strings.Join(arms, " "), target)
		}
	}
}

// The labels of the one `case` that dispatches on a release target, in file
// order, `*` arms included.
func targetCaseArms(t *testing.T, file string) []string {
	t.Helper()

	text := readText(t, runfilePath(file))
	start := strings.Index(text, `case "$target" in`)
	if start < 0 {
		t.Fatalf("%s no longer dispatches on a release target", file)
	}
	block := text[start:]
	if end := strings.Index(block, "esac"); end >= 0 {
		block = block[:end]
	}

	var arms []string
	for _, match := range regexp.MustCompile(`(?m)^[ \t]*([^\s()|]+)\)`).FindAllStringSubmatch(block, -1) {
		arms = append(arms, match[1])
	}
	if len(arms) == 0 {
		t.Fatalf("%s dispatches on a release target with no arms", file)
	}
	return arms
}

func containerEnvValue(t *testing.T, file, name string) string {
	t.Helper()

	for _, doc := range yamlDocs(t, runfilePath(file)) {
		if stringValue(doc["kind"]) != "CronJob" {
			continue
		}
		podSpec := mapValue(mapValue(mapValue(mapValue(mapValue(doc["spec"])["jobTemplate"])["spec"])["template"])["spec"])
		for _, container := range sliceValue(podSpec["containers"]) {
			for _, env := range sliceValue(mapValue(container)["env"]) {
				if stringValue(mapValue(env)["name"]) == name {
					return stringValue(mapValue(env)["value"])
				}
			}
		}
	}
	t.Fatalf("%s declares no %s on any CronJob container", file, name)
	return ""
}

// magic and arch are the header bytes expected at offset 0 and at `at` for
// `size` bytes, hex as `od -tx1` prints it.
type objectShape struct {
	magic string
	arch  string
	at    string
	size  string
}

func (s objectShape) String() string {
	return "magic " + s.magic + " arch " + s.arch + " at " + s.at + " over " + s.size + " bytes"
}

// Both tables are `case` arms over $TARGETS embedded in YAML, one arm per
// target, so they are read the same way from either file.
func objectShapeTable(t *testing.T, file string) map[string]objectShape {
	t.Helper()

	arm := regexp.MustCompile(`(?m)^[ \t]*([a-z0-9_]+-[a-z0-9-]+)\)[ \t]+((?:[a-z]+=[0-9a-fx]+;?[ \t]*)+);;`)
	assignment := regexp.MustCompile(`([a-z]+)=([0-9a-fx]+)`)

	table := make(map[string]objectShape)
	for _, match := range arm.FindAllStringSubmatch(readText(t, runfilePath(file)), -1) {
		fields := make(map[string]string)
		for _, pair := range assignment.FindAllStringSubmatch(match[2], -1) {
			fields[pair[1]] = pair[2]
		}
		shape := objectShape{magic: fields["magic"], arch: fields["arch"], at: fields["at"], size: fields["len"]}
		if shape.magic == "" || shape.arch == "" || shape.at == "" || shape.size == "" {
			t.Fatalf("%s reads %s with an incomplete object shape (%q); magic, arch, at and len are all load-bearing",
				file, match[1], match[2])
		}
		table[match[1]] = shape
	}
	if len(table) == 0 {
		t.Fatalf("%s declares no per-target object shapes; the structural check is what stands in for running the binary", file)
	}
	return table
}

func shapedTargets(table map[string]objectShape) []string {
	targets := make([]string, 0, len(table))
	for target := range table {
		targets = append(targets, target)
	}
	return targets
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

	const path = "src/postflight/cli/dist/npm/postflight/bin/postflight"
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

	const path = "src/postflight/cli/dist/npm/postflight/package.json"
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

	path := "src/postflight/cli/dist/npm/postflight-" + suffix + "/package.json"
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

	const path = "src/postflight/cli/dist/homebrew/postflight.rb.tmpl"
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

// The smoke gate proves delegation by the absence of install.sh's
// removal-fallback log line, so the grepped sentinel and the logged line must
// stay one string: reworded independently, the gate goes vacuous instead of
// red for a binary whose `self uninstall` is broken.
func TestSmokeGateUninstallSentinelIsBound(t *testing.T) {
	const (
		imageWorkflow = ".github/workflows/postflight-cli-image.yml"
		installer     = "src/postflight/cli/dist/install.sh"
	)

	workflow := readText(t, runfilePath(imageWorkflow))
	match := regexp.MustCompile(`!= \*"([^"]+)"\*`).FindStringSubmatch(workflow)
	if match == nil {
		t.Fatalf("%s no longer greps a delegation sentinel; the uninstall leg of the smoke gate has lost its teeth", imageWorkflow)
	}
	sentinel := match[1]

	if !strings.Contains(readText(t, runfilePath(installer)), sentinel) {
		t.Fatalf("%s greps for %q but %s never logs that line; the delegation assert is vacuous", imageWorkflow, sentinel, installer)
	}
}
