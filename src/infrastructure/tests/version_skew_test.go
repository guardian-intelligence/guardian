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

const tofuHelmReleaseRunfile = "src/infrastructure/deployments/guardian/tofu/tofu-controller-helmrelease.yaml"

// The tofu-controller release rides three pins in one manifest: the chart
// GitRepository tag, the controller image newTag+digest postRenderer, and
// the runner image tag (digest embedded, because the controller uses the
// composed repository:tag verbatim as the runner pod image). Renovate moves
// them as one grouped PR; this holds a half-moved set red.
func TestTofuControllerPinsMoveTogether(t *testing.T) {
	raw := readText(t, runfilePath(tofuHelmReleaseRunfile))

	re := regexp.MustCompile(`(?m)^\s*# renovate: tofu-controller-release\n\s*(?:tag|newTag): "?(v[0-9]+\.[0-9]+\.[0-9]+)`)
	matches := re.FindAllStringSubmatch(raw, -1)
	if len(matches) != 3 {
		t.Fatalf("%s: expected 3 renovate-annotated tofu-controller-release pins (GitRepository tag, runner tag, postRenderers newTag), found %d", tofuHelmReleaseRunfile, len(matches))
	}
	want := matches[0][1]
	for _, m := range matches[1:] {
		if m[1] != want {
			t.Fatalf("%s: tofu-controller release pins disagree: %q vs %q — move the GitRepository tag, runner image, and postRenderers digest together", tofuHelmReleaseRunfile, want, m[1])
		}
	}
}

// The runner image bundles its own OpenTofu (runner.Dockerfile
// ARG TOFU_VERSION), declared next to the pin as a runner-bundled-tofu
// comment. It must share a minor with the multitool tofu the break-glass
// workstation path uses, or the two plan differently against the same
// state. Patch drift within the minor is accepted — upstream pins its own
// patch.
func TestTofuRunnerTracksMultitoolPin(t *testing.T) {
	runner := extractMinor(t, tofuHelmReleaseRunfile,
		`# runner-bundled-tofu: ([0-9]+)\.([0-9]+)`)
	multitool := extractMinor(t, toolLockRunfile,
		`opentofu/releases/download/v([0-9]+)\.([0-9]+)\.[0-9]+/`)
	if runner != multitool {
		t.Fatalf("runner-bundled tofu %s and multitool tofu %s must share a minor: align the tf-runner release's bundled OpenTofu (runner.Dockerfile) with the multitool pin, and update the runner-bundled-tofu declaration when the images move", runner, multitool)
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
