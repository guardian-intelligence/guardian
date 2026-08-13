package tests

// Version-skew conformance for cluster-coupled tool pins: a client CLI in
// src/tools may not drift past what its cluster component supports, so a
// Renovate bump PR for one side cannot merge without the paired move.
//
//   - kubectl tracks the talm Chart.yaml kubernetesVersion within ±1 minor
//     (upstream kubectl skew policy).
//   - talosctl tracks the Talos installer image minor in the talm values:
//     equal, or one minor ahead during an upgrade window (talosctl vN
//     manages Talos vN and vN-1; an older talosctl against a newer Talos is
//     unsupported). Bump talosctl first, then the installer image.
//   - The talm Chart.yaml talosVersion minor must agree with the installer
//     image minor — two spellings of the same substrate fact.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	toolLockRunfile   = "src/tools/multitool.lock.json"
	talmChartRunfile  = "src/infrastructure/talm/Chart.yaml"
	talmValuesRunfile = "src/infrastructure/talm/values.yaml"
)

type minorVersion struct {
	major int
	minor int
}

func (v minorVersion) String() string {
	return fmt.Sprintf("%d.%d", v.major, v.minor)
}

// extractMinor returns the single distinct major.minor the pattern's two
// capture groups match in the named runfile, failing on zero or conflicting
// matches so a half-edited pin (e.g. one of kubectl's two platform URLs)
// cannot slip through.
func extractMinor(t *testing.T, runfile, pattern string) minorVersion {
	t.Helper()
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(readText(t, runfilePath(runfile)), -1)
	if len(matches) == 0 {
		t.Fatalf("%s: no match for %q", runfile, pattern)
	}
	var got minorVersion
	for i, m := range matches {
		major, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s: major %q: %v", runfile, m[1], err)
		}
		minor, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("%s: minor %q: %v", runfile, m[2], err)
		}
		v := minorVersion{major: major, minor: minor}
		if i > 0 && v != got {
			t.Fatalf("%s: conflicting versions %s and %s for %q", runfile, got, v, pattern)
		}
		got = v
	}
	return got
}

func TestKubectlTracksClusterKubernetesVersion(t *testing.T) {
	kubectl := extractMinor(t, toolLockRunfile,
		`dl\.k8s\.io/release/v(\d+)\.(\d+)\.\d+/bin/`)
	cluster := extractMinor(t, talmChartRunfile,
		`kubernetesVersion: "v(\d+)\.(\d+)\.\d+"`)

	if kubectl.major != cluster.major {
		t.Fatalf("kubectl pin v%s and cluster kubernetesVersion v%s differ in major version", kubectl, cluster)
	}
	if diff := kubectl.minor - cluster.minor; diff < -1 || diff > 1 {
		t.Fatalf("kubectl pin v%s is outside the supported ±1 minor skew of cluster kubernetesVersion v%s: move them together (the kubernetes substrate doorbell PR bumps both)", kubectl, cluster)
	}
}

func TestTalosctlTracksInstallerImage(t *testing.T) {
	talosctl := extractMinor(t, toolLockRunfile,
		`siderolabs/talos/releases/download/v(\d+)\.(\d+)\.\d+/talosctl`)
	installer := extractMinor(t, talmValuesRunfile,
		`image: "[^"]*:v(\d+)\.(\d+)\.\d+@sha256:`)

	if talosctl.major != installer.major {
		t.Fatalf("talosctl pin v%s and Talos installer image v%s differ in major version", talosctl, installer)
	}
	if diff := talosctl.minor - installer.minor; diff < 0 || diff > 1 {
		t.Fatalf("talosctl pin v%s must be at the Talos installer image minor v%s or one ahead: bump talosctl first, then the installer image", talosctl, installer)
	}
}

// The countersigner extracts its cosign binary from the pinned cosign image;
// the multitool release binary is that image's trust anchor (verified
// keyless at adoption) and the dark-drive copy verifies what the
// countersigner signs — so all three cosign pins move together: a bump PR
// for any one of them goes red until the others follow.
func TestCountersignerCosignPinsMoveTogether(t *testing.T) {
	countersignerYAML := "src/infrastructure/deployments/guardian/system/zot-countersigner.yaml"
	declaredLock := "src/infrastructure/bootstrap/bundle/images.declared.lock"

	release := regexp.MustCompile(`sigstore/cosign/releases/download/(v\d+\.\d+\.\d+)/cosign-linux-amd64`).
		FindStringSubmatch(readText(t, runfilePath(toolLockRunfile)))
	if release == nil {
		t.Fatalf("%s: no cosign release pin found", toolLockRunfile)
	}
	image := regexp.MustCompile(`ghcr\.io/sigstore/cosign/cosign:(v\d+\.\d+\.\d+)@(sha256:[a-f0-9]{64})`).
		FindStringSubmatch(readText(t, runfilePath(countersignerYAML)))
	if image == nil {
		t.Fatalf("%s: no COSIGN_IMAGE tag@digest pin found", countersignerYAML)
	}
	declared := regexp.MustCompile(`ghcr\.io/sigstore/cosign/cosign@(sha256:[a-f0-9]{64})`).
		FindStringSubmatch(readText(t, runfilePath(declaredLock)))
	if declared == nil {
		t.Fatalf("%s: no cosign image digest declared", declaredLock)
	}

	if image[1] != release[1] {
		t.Fatalf("countersigner COSIGN_IMAGE is %s but the multitool cosign release pin is %s: move them together", image[1], release[1])
	}
	if image[2] != declared[1] {
		t.Fatalf("countersigner COSIGN_IMAGE digest %s and the images.declared.lock entry %s differ: the dark haul would carry a different image than the countersigner fetches", image[2], declared[1])
	}
}

// The release projector extracts cosign and regctl the same way the
// countersigner extracts cosign, with the same anchoring: the multitool
// regctl release binary is the regctl image's trust anchor, the declared
// lock carries what the pod fetches, and the projector's cosign pin may
// never drift from the countersigner's — one cosign verifies what the other
// signs.
func TestReleaseProjectorToolPinsMoveTogether(t *testing.T) {
	projectorYAML := "src/infrastructure/deployments/guardian/system/release-projector.yaml"
	countersignerYAML := "src/infrastructure/deployments/guardian/system/zot-countersigner.yaml"
	declaredLock := "src/infrastructure/bootstrap/bundle/images.declared.lock"

	release := regexp.MustCompile(`regclient/regclient/releases/download/(v\d+\.\d+\.\d+)/regctl-linux-amd64`).
		FindStringSubmatch(readText(t, runfilePath(toolLockRunfile)))
	if release == nil {
		t.Fatalf("%s: no regctl release pin found", toolLockRunfile)
	}
	image := regexp.MustCompile(`ghcr\.io/regclient/regctl:(v\d+\.\d+\.\d+)@(sha256:[a-f0-9]{64})`).
		FindStringSubmatch(readText(t, runfilePath(projectorYAML)))
	if image == nil {
		t.Fatalf("%s: no REGCTL_IMAGE tag@digest pin found", projectorYAML)
	}
	declared := regexp.MustCompile(`ghcr\.io/regclient/regctl@(sha256:[a-f0-9]{64})`).
		FindStringSubmatch(readText(t, runfilePath(declaredLock)))
	if declared == nil {
		t.Fatalf("%s: no regctl image digest declared", declaredLock)
	}
	if image[1] != release[1] {
		t.Fatalf("projector REGCTL_IMAGE is %s but the multitool regctl release pin is %s: move them together", image[1], release[1])
	}
	if image[2] != declared[1] {
		t.Fatalf("projector REGCTL_IMAGE digest %s and the images.declared.lock entry %s differ: the dark haul would carry a different image than the projector fetches", image[2], declared[1])
	}

	cosignPin := regexp.MustCompile(`ghcr\.io/sigstore/cosign/cosign:v\d+\.\d+\.\d+@sha256:[a-f0-9]{64}`)
	projectorCosign := cosignPin.FindString(readText(t, runfilePath(projectorYAML)))
	countersignerCosign := cosignPin.FindString(readText(t, runfilePath(countersignerYAML)))
	if projectorCosign == "" {
		t.Fatalf("%s: no COSIGN_IMAGE tag@digest pin found", projectorYAML)
	}
	if projectorCosign != countersignerCosign {
		t.Fatalf("projector COSIGN_IMAGE %s and countersigner COSIGN_IMAGE %s differ: move them together", projectorCosign, countersignerCosign)
	}
}

// The CLI deep-test runner extracts cosign the same way and verifies the
// same release lane's signatures, so its pin joins the countersigner's and
// the projector's: one cosign may not judge what another cosign version
// signed.
func TestDeeptestRunnerCosignPinMatchesTheRegistryLane(t *testing.T) {
	runnerYAML := "src/infrastructure/deployments/guardian/promotion/cli-deeptest-runner.yaml"
	countersignerYAML := "src/infrastructure/deployments/guardian/system/zot-countersigner.yaml"

	cosignPin := regexp.MustCompile(`ghcr\.io/sigstore/cosign/cosign:v\d+\.\d+\.\d+@sha256:[a-f0-9]{64}`)
	runnerCosign := cosignPin.FindString(readText(t, runfilePath(runnerYAML)))
	countersignerCosign := cosignPin.FindString(readText(t, runfilePath(countersignerYAML)))
	if runnerCosign == "" {
		t.Fatalf("%s: no COSIGN_IMAGE tag@digest pin found", runnerYAML)
	}
	if runnerCosign != countersignerCosign {
		t.Fatalf("deep-test runner COSIGN_IMAGE %s and countersigner COSIGN_IMAGE %s differ: move them together", runnerCosign, countersignerCosign)
	}
}

// The install canary verifies published release assets with a cosign
// extracted from the same pinned image the countersigner signs with: the
// cosign that checks a signature may never drift from the cosign that mints
// it, so a bump PR for one goes red until the other follows.
func TestInstallCanaryCosignPinMatchesCountersigner(t *testing.T) {
	canaryYAML := "src/infrastructure/deployments/guardian/promotion/cli-install-canary.yaml"
	countersignerYAML := "src/infrastructure/deployments/guardian/system/zot-countersigner.yaml"

	cosignPin := regexp.MustCompile(`ghcr\.io/sigstore/cosign/cosign:v\d+\.\d+\.\d+@sha256:[a-f0-9]{64}`)
	canaryCosign := cosignPin.FindString(readText(t, runfilePath(canaryYAML)))
	countersignerCosign := cosignPin.FindString(readText(t, runfilePath(countersignerYAML)))
	if canaryCosign == "" {
		t.Fatalf("%s: no COSIGN_IMAGE tag@digest pin found", canaryYAML)
	}
	if canaryCosign != countersignerCosign {
		t.Fatalf("install canary COSIGN_IMAGE %s and countersigner COSIGN_IMAGE %s differ: move them together", canaryCosign, countersignerCosign)
	}
}

// TARGETS, BUILD_IDENTITY and ISSUER name what the release cutter signs and
// under which identity. The install canary re-verifies exactly that set, and
// a target added to the cutter alone would still be downloaded and
// checksummed by the canary while never being verify-blob'd or asserted for
// object format — a green cell over an unverified artifact.
func TestInstallCanaryReleaseContractMatchesCutter(t *testing.T) {
	canaryYAML := "src/infrastructure/deployments/guardian/promotion/cli-install-canary.yaml"
	cutterYAML := ".github/workflows/postflight-cli-release.yml"
	canary := readText(t, runfilePath(canaryYAML))
	cutter := readText(t, runfilePath(cutterYAML))

	for _, key := range []string{"TARGETS", "BUILD_IDENTITY", "ISSUER"} {
		want := scalarValue(t, cutter, cutterYAML, key,
			regexp.MustCompile(`(?m)^ {2}`+key+`:[ \t]+(\S.*?)[ \t]*$`))
		got := scalarValue(t, canary, canaryYAML, key,
			regexp.MustCompile(`(?m)^[ \t]*- name: `+key+`[ \t]*\n[ \t]*value:[ \t]+(\S.*?)[ \t]*$`))
		if got != want {
			t.Fatalf("install canary %s = %q but release cutter %s = %q: move them together", key, got, key, want)
		}
	}
}

const (
	tofuManifestDirRunfile = "src/infrastructure/deployments/guardian/tofu/kustomization.yaml"
	moduleBazelRunfile     = "MODULE.bazel"
)

// Every per-root CronJob pins the tofu-runner image by digest and carries the
// image-automation marker, so Flux moves all six to a new digest in one
// commit and never leaves a root running a stale runner.
var tofuRunnerImageRe = regexp.MustCompile(
	`image:\s*(ghcr\.io/guardian-intelligence/tofu-runner:edge@sha256:[0-9a-f]{64})\s*#\s*\{"\$imagepolicy":\s*"guardian-imageops:tofu-runner"\}`)

func TestTofuRunnerImagePinsAgree(t *testing.T) {
	dir := filepath.Dir(runfilePath(tofuManifestDirRunfile))
	files, err := filepath.Glob(filepath.Join(dir, "cronjob-*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 6 {
		t.Fatalf("expected 6 tofu-runner CronJob manifests, found %d", len(files))
	}
	var want string
	for _, f := range files {
		m := tofuRunnerImageRe.FindStringSubmatch(readText(t, f))
		if m == nil {
			t.Fatalf("%s: no digest-pinned tofu-runner image with a guardian-imageops:tofu-runner imagepolicy marker", filepath.Base(f))
		}
		if want == "" {
			want = m[1]
		}
		if m[1] != want {
			t.Fatalf("%s pins %q but another CronJob pins %q — all roots must share one tofu-runner digest so image automation moves them together", filepath.Base(f), m[1], want)
		}
	}
}

// The tofu the runner image ships and the multitool tofu the break-glass
// workstation path uses come from the same OpenTofu release: an in-cluster
// apply and a hand apply against the same state must run identical tofu, so
// the two pins must be the exact same version, not merely the same minor.
func TestTofuRunnerTracksMultitoolPin(t *testing.T) {
	image := tofuLinuxVersion(t, moduleBazelRunfile)
	multitool := tofuLinuxVersion(t, toolLockRunfile)
	if image != multitool {
		t.Fatalf("runner image tofu %s and multitool tofu %s must be the same version: move the opentofu_linux_amd64 pin in MODULE.bazel and the multitool tofu pin together", image, multitool)
	}
}

func tofuLinuxVersion(t *testing.T, runfile string) string {
	t.Helper()
	re := regexp.MustCompile(`opentofu/releases/download/v([0-9]+\.[0-9]+\.[0-9]+)/tofu_[0-9.]+_linux_amd64\.zip`)
	m := re.FindStringSubmatch(readText(t, runfilePath(runfile)))
	if m == nil {
		t.Fatalf("%s: no linux_amd64 opentofu release pin found", runfile)
	}
	return m[1]
}

const tofuRunnerBuildRunfile = "src/infrastructure/cmd/tofu_runner/BUILD.bazel"

// tofuRootNames derives the root set from the deployed CronJob manifests, so
// a root added to the cluster cannot be forgotten here: its lockfile is
// either in this test's runfiles (add the //src/infrastructure/bootstrap/…
// :root data dep) or readText fails loudly.
func tofuRootNames(t *testing.T) []string {
	t.Helper()
	dir := filepath.Dir(runfilePath(tofuManifestDirRunfile))
	files, err := filepath.Glob(filepath.Join(dir, "cronjob-*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("%s: no cronjob-*.yaml manifests found", dir)
	}
	var roots []string
	for _, f := range files {
		roots = append(roots, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "cronjob-"), ".yaml"))
	}
	return roots
}

// The runner image bakes a packed provider filesystem mirror and its tofurc
// has no direct{} fallback, so a provider a root needs but the mirror lacks
// fails init in-cluster. A provider pin therefore lives in four places that
// must move together: the root's versions.tf constraint, its
// .terraform.lock.hcl, the MODULE.bazel mirror archive, and the mirror
// layout list in the runner's BUILD file. Renovate rewrites only versions.tf
// and cannot regenerate the lockfile or recompute the mirror pins, so this
// test is the doorbell: the bump PR goes red until all four move together.
// The mirror sha256 must also be one of the lockfile's zh: hashes, so the
// image can only ship bytes the root already trusts — tofu re-verifies the
// same hash at init.
func TestTofuRunnerProviderMirrorTracksRootLockfiles(t *testing.T) {
	type mirrorPin struct {
		version string
		sha256  string
	}
	pinRe := regexp.MustCompile(
		`(?s)downloaded_file_path = "terraform-provider-([a-z0-9]+)_([0-9.]+)_linux_amd64\.zip",\s*sha256 = "([0-9a-f]{64})",`)
	pins := map[string]mirrorPin{}
	for _, m := range pinRe.FindAllStringSubmatch(readText(t, runfilePath(moduleBazelRunfile)), -1) {
		if _, dup := pins[m[1]]; dup {
			t.Fatalf("MODULE.bazel: two mirror pins for provider type %q", m[1])
		}
		pins[m[1]] = mirrorPin{version: m[2], sha256: m[3]}
	}
	if len(pins) == 0 {
		t.Fatalf("%s: no tofu provider mirror pins found", moduleBazelRunfile)
	}

	// The (name, address) pairs of the BUILD file's TOFU_PROVIDER_MIRROR
	// list, from which the image's mirror layout is derived. The name selects
	// the zip whose filename embeds the provider type tofu discovers by, so
	// it must equal the address's type segment or the zip lands under a
	// directory tofu never consults.
	layout := map[string]bool{}
	pairRe := regexp.MustCompile(`\("([a-z0-9]+)", "([a-z0-9-]+/[a-z0-9-]+)"\)`)
	for _, m := range pairRe.FindAllStringSubmatch(readText(t, runfilePath(tofuRunnerBuildRunfile)), -1) {
		name, address := m[1], m[2]
		if name != address[strings.LastIndex(address, "/")+1:] {
			t.Fatalf("%s: mirror pair (%q, %q): the short name must be the address's type segment", tofuRunnerBuildRunfile, name, address)
		}
		if _, ok := pins[name]; !ok {
			t.Fatalf("%s lays out provider %s but MODULE.bazel has no tofu_provider_%s_linux_amd64 pin", tofuRunnerBuildRunfile, address, name)
		}
		layout[address] = true
	}

	lockHostRe := regexp.MustCompile(`(?m)^provider "([^"]+)" \{`)
	providerBlockRe := regexp.MustCompile(`(?s)provider "registry\.opentofu\.org/([a-z0-9-]+/[a-z0-9-]+)" \{(.*?)\n\}`)
	lockVersionRe := regexp.MustCompile(`version\s*=\s*"([0-9.]+)"`)
	zhRe := regexp.MustCompile(`"zh:([0-9a-f]{64})"`)
	declaredRe := regexp.MustCompile(`source\s*=\s*"([^"]+)"\s*\n\s*version\s*=\s*"([^"]+)"`)

	locked := map[string]bool{}
	for _, root := range tofuRootNames(t) {
		lockfile := "src/infrastructure/bootstrap/" + root + "/.terraform.lock.hcl"
		lockText := readText(t, runfilePath(lockfile))
		for _, m := range lockHostRe.FindAllStringSubmatch(lockText, -1) {
			if !strings.HasPrefix(m[1], "registry.opentofu.org/") {
				t.Fatalf("%s locks provider %q outside registry.opentofu.org: the baked mirror and this doorbell only cover that host — renormalize the source address", lockfile, m[1])
			}
		}
		blocks := providerBlockRe.FindAllStringSubmatch(lockText, -1)
		if len(blocks) == 0 {
			t.Fatalf("%s: no provider blocks found", lockfile)
		}
		for _, block := range blocks {
			address := block[1]
			providerType := address[strings.LastIndex(address, "/")+1:]
			pin, ok := pins[providerType]
			if !ok {
				t.Fatalf("%s locks %s but MODULE.bazel has no tofu_provider_%s_linux_amd64 mirror pin: the runner image could not init this root", root, address, providerType)
			}
			version := lockVersionRe.FindStringSubmatch(block[2])
			if version == nil {
				t.Fatalf("%s: no version in the %s block", lockfile, address)
			}
			if version[1] != pin.version {
				t.Fatalf("%s locks %s %s but the MODULE.bazel mirror ships %s: move the lockfile and the mirror pin together", root, address, version[1], pin.version)
			}
			trusted := false
			for _, zh := range zhRe.FindAllStringSubmatch(block[2], -1) {
				trusted = trusted || zh[1] == pin.sha256
			}
			if !trusted {
				t.Fatalf("MODULE.bazel mirror sha256 %s for %s is not among %s's zh: lockfile hashes: the image would ship a zip the root does not trust", pin.sha256, address, root)
			}
			if !layout[address] {
				t.Fatalf("%s locks %s but the TOFU_PROVIDER_MIRROR list in %s does not lay its zip into the image", root, address, tofuRunnerBuildRunfile)
			}
			locked[providerType] = true
		}

		// The declared constraint is what Renovate rewrites; the mirror can
		// only serve the exact locked version, so the constraint must BE that
		// version — a bumped-but-not-relocked root fails here, not in-cluster.
		declared := declaredRe.FindAllStringSubmatch(readText(t, runfilePath("src/infrastructure/bootstrap/"+root+"/versions.tf")), -1)
		if len(declared) == 0 {
			t.Fatalf("%s/versions.tf: no required_providers source/version pairs found", root)
		}
		for _, d := range declared {
			address := strings.TrimPrefix(d[1], "registry.opentofu.org/")
			providerType := address[strings.LastIndex(address, "/")+1:]
			pin, ok := pins[providerType]
			if !ok {
				t.Fatalf("%s/versions.tf declares provider %s with no MODULE.bazel mirror pin", root, d[1])
			}
			constraint := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d[2]), "="))
			if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(constraint) {
				t.Fatalf("%s/versions.tf pins %s as %q: the baked mirror requires an exact version pin", root, address, d[2])
			}
			if constraint != pin.version {
				t.Fatalf("%s/versions.tf pins %s %s but the MODULE.bazel mirror ships %s: regenerate the lockfile (tofu init -upgrade) and move the mirror pin together", root, address, constraint, pin.version)
			}
		}
	}
	for providerType := range pins {
		if !locked[providerType] {
			t.Fatalf("MODULE.bazel mirror pin tofu_provider_%s_linux_amd64 matches no root's lockfile: prune it or fix its zip name", providerType)
		}
	}
}

// TofuRootNeverSucceeded pages on count(last-successful series) < N with N
// hand-written in the VMRule; a root added without bumping N would fail
// invisibly forever (its failed runs have no last-success series for
// TofuRootJobFailed's join to compare against). Bind N to the deployed
// CronJob count so the manifest add and the rule bump are one PR.
func TestTofuRootNeverSucceededRuleCountsAllRoots(t *testing.T) {
	observability := filepath.Join(filepath.Dir(runfilePath(tofuManifestDirRunfile)), "observability.yaml")
	m := regexp.MustCompile(`count\(kube_cronjob_status_last_successful_time\{namespace="tofu-system"\}\) < ([0-9]+)`).
		FindStringSubmatch(readText(t, observability))
	if m == nil {
		t.Fatalf("%s: no TofuRootNeverSucceeded count threshold found", observability)
	}
	if want := len(tofuRootNames(t)); m[1] != strconv.Itoa(want) {
		t.Fatalf("TofuRootNeverSucceeded expects %s roots but %d CronJob manifests exist: move the rule's count with the root set", m[1], want)
	}
}

func scalarValue(t *testing.T, raw, path, key string, re *regexp.Regexp) string {
	t.Helper()
	match := re.FindStringSubmatch(raw)
	if match == nil {
		t.Fatalf("%s: no %s value found", path, key)
	}
	return strings.Trim(match[1], `"'`)
}

func TestTalmChartTalosVersionAgreesWithInstallerImage(t *testing.T) {
	chart := extractMinor(t, talmChartRunfile,
		`talosVersion: "v(\d+)\.(\d+)"`)
	installer := extractMinor(t, talmValuesRunfile,
		`image: "[^"]*:v(\d+)\.(\d+)\.\d+@sha256:`)

	if chart != installer {
		t.Fatalf("talm Chart.yaml talosVersion v%s and the Talos installer image v%s state different substrate versions: they move together in the Talos upgrade runbook", chart, installer)
	}
}
