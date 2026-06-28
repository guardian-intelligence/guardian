package tests

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
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
		"image: ghcr.io/cozystack/cozystack/talos:v1.13.0",
		"serviceSubnets:\n      - 10.96.0.0/16",
		"cluster-cidr: 10.244.0.0/16",
		"advertisedSubnets:\n      - 10.8.0.0/24",
		"kind: Layer2VIPConfig",
		"name: \"10.8.0.250\"",
		"link: enp1s0f0.2140",
		"guardian.dev/openbao-seal: \"true\"",
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

	for _, forbidden := range []string{"kubespan", "KubeSpan", "WireGuard", "wireguard"} {
		assertTextNotContains(t, rendered, forbidden, "rendered controlplane talos config")
	}
}

func talmBinary(t *testing.T) string {
	t.Helper()

	path, err := runfiles.Rlocation("talm_linux_amd64/talm")
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
