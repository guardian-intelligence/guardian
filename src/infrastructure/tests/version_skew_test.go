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
		`ghcr\.io/cozystack/cozystack/talos:v(\d+)\.(\d+)\.\d+@sha256:`)

	if talosctl.major != installer.major {
		t.Fatalf("talosctl pin v%s and Talos installer image v%s differ in major version", talosctl, installer)
	}
	if diff := talosctl.minor - installer.minor; diff < 0 || diff > 1 {
		t.Fatalf("talosctl pin v%s must be at the Talos installer image minor v%s or one ahead: bump talosctl first, then the installer image", talosctl, installer)
	}
}

func TestTalmChartTalosVersionAgreesWithInstallerImage(t *testing.T) {
	chart := extractMinor(t, talmChartRunfile,
		`talosVersion: "v(\d+)\.(\d+)"`)
	installer := extractMinor(t, talmValuesRunfile,
		`ghcr\.io/cozystack/cozystack/talos:v(\d+)\.(\d+)\.\d+@sha256:`)

	if chart != installer {
		t.Fatalf("talm Chart.yaml talosVersion v%s and the Talos installer image v%s state different substrate versions: they move together in the Talos upgrade runbook", chart, installer)
	}
}
