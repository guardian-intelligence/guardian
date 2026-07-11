package tests

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/guardian-intelligence/guardian/src/infrastructure/imageset"
)

func TestTalmControlplaneRender(t *testing.T) {
	scratch := t.TempDir()
	talm := talmBinary(t)
	env := isolatedTalmEnv(t, scratch)

	runTalm(t, talm, env,
		"init",
		"--root", scratch,
		"--name", "guardian-mgmt",
		"--preset", "cozystack",
		"--cluster-endpoint", "https://10.8.0.250:6443",
		"--talos-version", "v1.13.0",
		"--force",
	)

	chartRoot := filepath.Dir(runfilePath("src/infrastructure/talm/values.yaml"))
	rendered := runTalm(t, talm, env,
		"template",
		"--offline",
		"--root", chartRoot,
		"--values", filepath.Join(chartRoot, "values.yaml"),
		"--template", filepath.Join(chartRoot, "templates/controlplane.yaml"),
		"--with-secrets", filepath.Join(scratch, "secrets.yaml"),
		"--talos-version", "v1.13.0",
		"--kubernetes-version", "v1.34.3",
	)

	for _, want := range []string{
		"clusterName: guardian-mgmt",
		"endpoint: https://10.8.0.250:6443",
		"image: ghcr.io/cozystack/cozystack/talos:v1.13.0@sha256:c2c092ad742e8bdd4af6366c586d95bdf7f73cee6eef8318fb9da6d466f37044",
		"url: https://mirror.gcr.io",
		"serviceSubnets:\n      - 10.96.0.0/16",
		"cluster-cidr: 10.244.0.0/16",
		"advertisedSubnets:\n      - 10.8.0.0/24",
		"listen-metrics-urls: http://127.0.0.1:2381",
		"kind: Layer2VIPConfig",
		"name: \"10.8.0.250\"",
		"link: enp1s0f0.2140",
		"guardian.dev/openbao-static-seal: \"true\"",
		"kind: WatchdogTimerConfig",
		"device: /dev/watchdog0",
		// Offline discovery has no disks, so the install pin falls back to
		// the device name. Online regen must emit diskSelector.serial — a
		// bare /dev/nvmeXn1 name can point at a different physical disk on
		// the next boot, and install.disk is consulted exactly at reimage.
		"disk: /dev/sda",
	} {
		assertTextContains(t, rendered, want, "rendered controlplane talos config")
	}

	for _, want := range []string{
		"api.guardianintelligence.org",
		"10.8.0.250",
		"206.223.228.101",
		"45.250.254.119",
		"206.223.228.87",
	} {
		assertTextContains(t, rendered, want, "rendered controlplane cert SANs")
	}

	// A register-with-taints NoSchedule taint on every node bricks cold
	// bootstrap (nothing, including the Cozystack installer hook, can
	// schedule); the 2026-07-01 drill hit this. Dedicated-node taints may
	// return only alongside untainted general-workload nodes. skipFallback,
	// the haul mirror endpoint, and the mirror-host NTP block are
	// dark-bootstrap-only: in steady state they would cut nodes off from
	// upstream registries and public time. The bare mirror-host IP is NOT
	// forbidden — the same box is the operations VPS and appears in the
	// steady-state ingress-firewall operator rules.
	for _, forbidden := range []string{"kubespan", "KubeSpan", "WireGuard", "wireguard", "nodeTaints:", "skipFallback", "148.113.198.223:5000", "\n  time:\n"} {
		assertTextNotContains(t, rendered, forbidden, "rendered controlplane talos config")
	}
}

// TestTalmDarkBundleMirrorRender proves the dark-bootstrap values state:
// every locked upstream registry is mirrored to the haul-served registry
// with skipFallback, node NTP points at the mirror host, and the steady
// mirror.gcr.io fallback for docker.io is replaced (a duplicate docker.io
// mirror document would be invalid).
func TestTalmDarkBundleMirrorRender(t *testing.T) {
	scratch := t.TempDir()
	talm := talmBinary(t)
	env := isolatedTalmEnv(t, scratch)

	runTalm(t, talm, env,
		"init",
		"--root", scratch,
		"--name", "guardian-mgmt",
		"--preset", "cozystack",
		"--cluster-endpoint", "https://10.8.0.250:6443",
		"--talos-version", "v1.13.0",
		"--force",
	)

	chartRoot := filepath.Dir(runfilePath("src/infrastructure/talm/values.yaml"))
	darkValues := filepath.Join(scratch, "values-dark.yaml")
	if err := os.WriteFile(darkValues, []byte("darkBundleMirror:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write dark values override: %v", err)
	}
	rendered := runTalm(t, talm, env,
		"template",
		"--offline",
		"--root", chartRoot,
		"--values", filepath.Join(chartRoot, "values.yaml"),
		"--values", darkValues,
		"--template", filepath.Join(chartRoot, "templates/controlplane.yaml"),
		"--with-secrets", filepath.Join(scratch, "secrets.yaml"),
		"--talos-version", "v1.13.0",
		"--kubernetes-version", "v1.34.3",
	)

	lockHosts := imagesLockHosts(t)
	if len(lockHosts) < 5 {
		t.Fatalf("images.lock yielded only %d registry hosts; expected the full upstream set", len(lockHosts))
	}
	for _, host := range lockHosts {
		assertTextContains(t, rendered, "name: "+host, "dark render mirrors every locked registry")
	}
	for _, want := range []string{
		"url: http://148.113.198.223:5000",
		"skipFallback: true",
		"time:\n    servers:\n      - 148.113.198.223",
	} {
		assertTextContains(t, rendered, want, "dark render")
	}
	assertTextNotContains(t, rendered, "mirror.gcr.io", "dark render must not fall back to public mirrors")
}

// unionLockEntries derives the complete inventory (declared + rendered)
// exactly as the imageset generator does.
func unionLockEntries(t *testing.T) []string {
	t.Helper()

	union, err := imageset.Union(declaredLockEntries(t), renderedLockEntries(t))
	if err != nil {
		t.Fatal(err)
	}
	return union
}

// imagesLockHosts returns the sorted, unique registry hosts referenced by
// the union inventory. The darkBundleMirror.registries list in values.yaml
// must equal this set: an upstream host missing from the dark mirrors would
// make nodes dial the internet (or fail) for a locked artifact.
func imagesLockHosts(t *testing.T) []string {
	t.Helper()

	hosts, err := imageset.Hosts(unionLockEntries(t))
	if err != nil {
		t.Fatal(err)
	}
	return hosts
}

// The dark mirror serves every host in one flat, host-stripped namespace
// (registry.k8s.io/pause and ghcr.io/x both become /v2/...), so two lock
// refs from different hosts that share a repository path would collide on
// the same served path with different content. Guard against it.
func TestImagesLockNoHostStrippedPathCollision(t *testing.T) {
	seen := map[string]string{} // stripped repo path -> first full repo
	for _, ref := range unionLockEntries(t) {
		nameAndTag, _, _ := strings.Cut(ref, "@")
		repo := nameAndTag
		if colon := strings.LastIndex(nameAndTag, ":"); colon > strings.LastIndex(nameAndTag, "/") {
			repo = nameAndTag[:colon]
		}
		_, stripped, _ := strings.Cut(repo, "/") // drop the registry host
		if prior, ok := seen[stripped]; ok && prior != repo {
			t.Fatalf("dark mirror path collision on /%s: %q and %q map to the same served path", stripped, prior, repo)
		}
		seen[stripped] = repo
	}
}

func TestDarkBundleMirrorRegistriesMatchImagesLock(t *testing.T) {
	values := singleYAMLDoc(t, runfilePath("src/infrastructure/talm/values.yaml"))
	dark := mapValue(values["darkBundleMirror"])
	var declared []string
	for _, host := range sliceValue(dark["registries"]) {
		declared = append(declared, stringValue(host))
	}
	sort.Strings(declared)
	lockHosts := imagesLockHosts(t)
	if !reflect.DeepEqual(declared, lockHosts) {
		t.Fatalf("values.yaml darkBundleMirror.registries = %v, union lock hosts = %v; the dark mirror set must equal the locked upstreams", declared, lockHosts)
	}
}

func talmBinary(t *testing.T) string {
	t.Helper()

	path, err := runfiles.Rlocation("multitool/tools/talm/talm")
	if err != nil {
		t.Fatalf("locate pinned talm binary: %v", err)
	}
	return path
}

func runfilePath(path string) string {
	resolved, err := runfiles.Rlocation("_main/" + path)
	if err == nil {
		return resolved
	}
	resolved, err = runfiles.Rlocation(path)
	if err == nil {
		return resolved
	}
	return path
}

func assertTextContains(t *testing.T, text, want, context string) {
	t.Helper()

	if !strings.Contains(text, want) {
		t.Fatalf("%s does not contain %q", context, want)
	}
}

func assertTextNotContains(t *testing.T, text, forbidden, context string) {
	t.Helper()

	if strings.Contains(text, forbidden) {
		t.Fatalf("%s contains forbidden text %q", context, forbidden)
	}
}

func isolatedTalmEnv(t *testing.T, scratch string) []string {
	t.Helper()

	for _, dir := range []string{"home", "config", "cache", "state", "tmp"} {
		if err := os.MkdirAll(filepath.Join(scratch, dir), 0o700); err != nil {
			t.Fatalf("create talm scratch dir %s: %v", dir, err)
		}
	}

	runfilesEnv, err := runfiles.Env()
	if err != nil {
		t.Fatalf("build runfiles env: %v", err)
	}

	env := append([]string{}, os.Environ()...)
	env = append(env, runfilesEnv...)
	env = append(env,
		"HOME="+filepath.Join(scratch, "home"),
		"TMPDIR="+filepath.Join(scratch, "tmp"),
		"XDG_CONFIG_HOME="+filepath.Join(scratch, "config"),
		"XDG_CACHE_HOME="+filepath.Join(scratch, "cache"),
		"XDG_STATE_HOME="+filepath.Join(scratch, "state"),
	)
	return env
}

func runTalm(t *testing.T, talm string, env []string, args ...string) string {
	t.Helper()

	cmd := exec.Command(talm, args...)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("talm %s failed: %v\nstdout bytes: %d\nstderr:\n%s", args[0], err, stdout.Len(), capCommandOutput(stderr.String()))
	}

	return stdout.String()
}

func capCommandOutput(out string) string {
	const max = 4000
	out = strings.TrimSpace(out)
	if len(out) <= max {
		return out
	}
	return out[:max] + "\n... truncated ..."
}
