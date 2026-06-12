package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// guardian run executes version-pinned developer tools, replacing ad-hoc
// host installs. talosctl and kubectl are the pinned binaries riding in the
// guardian binary's runfiles. bazel is resolved bazelisk-style from the
// enclosing workspace's checked-in pins (.bazeliskrc, .bazelversion),
// downloaded once into ~/.cache/guardian/tools, sha256-verified on every
// invocation, and exec'd in place.
//
// `guardian tools install` symlinks these names at the guardian binary;
// main() routes a non-guardian argv[0] back through runNamedTool, so the
// symlinks behave as the tools themselves.

// managedTools is the full set of names `guardian run` accepts and
// `guardian tools install` symlinks (plus "guardian" itself).
var managedTools = []string{"aspect", "bazel", "talosctl", "kubectl"}

func isManagedTool(name string) bool {
	for _, t := range managedTools {
		if t == name {
			return true
		}
	}
	return false
}

func runRunCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("run: expected a tool name (one of %s)", strings.Join(managedTools, ", "))
	}
	tool, rest := args[0], args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	return runNamedTool(tool, rest)
}

// runNamedTool resolves a pinned tool and execs it, replacing this process.
// It returns only on failure.
func runNamedTool(tool string, args []string) error {
	var bin string
	var err error
	switch tool {
	case "aspect":
		bin, err = ensurePinnedAspect()
	case "bazel":
		bin, err = ensurePinnedBazel()
	case "talosctl":
		bin, err = talosctlPath()
	case "kubectl":
		bin, err = kubectlPath()
	default:
		return fmt.Errorf("run: unknown tool %q (one of %s)", tool, strings.Join(managedTools, ", "))
	}
	if err != nil {
		return err
	}
	return syscall.Exec(bin, append([]string{bin}, args...), os.Environ())
}

// workspaceRoot walks up from the working directory to the nearest Bazel
// workspace boundary. bazel pins are read from that directory, so the same
// guardian binary serves any checked-out workspace that carries pins.
func workspaceRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		for _, marker := range []string{"MODULE.bazel", "WORKSPACE", ".bazelversion"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("bazel: no workspace found above %s (looked for MODULE.bazel, WORKSPACE, .bazelversion)", mustGetwd())
		}
		dir = parent
	}
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "?"
	}
	return wd
}

// bazelPin reads the workspace's checked-in bazel pin. Version and binary
// sha256 come from .bazeliskrc (shared with bazelisk and the Aspect CLI),
// with .bazelversion as the version fallback. Environment overrides are
// deliberately not honored: changing what runs is a reviewed commit.
func bazelPin(root string) (version, sum string, err error) {
	if raw, rerr := os.ReadFile(filepath.Join(root, ".bazeliskrc")); rerr == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(line, "USE_BAZEL_VERSION="); ok {
				version = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "BAZELISK_VERIFY_SHA256="); ok {
				sum = strings.ToLower(strings.TrimSpace(v))
			}
		}
	}
	if version == "" {
		raw, rerr := os.ReadFile(filepath.Join(root, ".bazelversion"))
		if rerr != nil {
			return "", "", fmt.Errorf("bazel: no version pin in %s (.bazeliskrc or .bazelversion)", root)
		}
		version = strings.TrimSpace(string(raw))
	}
	if version == "" {
		return "", "", fmt.Errorf("bazel: empty version pin in %s", root)
	}
	return version, sum, nil
}

// ensurePinnedBazel returns a cached bazel binary matching the enclosing
// workspace's pin, downloading and verifying it on first use.
func ensurePinnedBazel() (string, error) {
	root, err := workspaceRoot()
	if err != nil {
		return "", err
	}
	version, sum, err := bazelPin(root)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://releases.bazel.build/%s/release/bazel-%s-linux-x86_64", version, version)
	return ensurePinnedTool("bazel", version, url, sum, root+"/.bazeliskrc (BAZELISK_VERIFY_SHA256)")
}

// aspectPin reads the workspace's Aspect CLI pin from .aspect/version.axl,
// the same file the aspect-launcher reads. The launcher format carries no
// integrity hash, so guardian additionally reads a `# guardian-sha256:` line.
func aspectPin(root string) (version, sum string, err error) {
	path := filepath.Join(root, ".aspect", "version.axl")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("aspect: no version pin (%s): %w", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, `version("`); ok {
			version = strings.TrimSuffix(strings.TrimSpace(v), `")`)
		}
		if v, ok := strings.CutPrefix(line, "# guardian-sha256:"); ok {
			sum = strings.ToLower(strings.TrimSpace(v))
		}
	}
	if version == "" {
		return "", "", fmt.Errorf("aspect: no version(\"...\") line in %s", path)
	}
	return version, sum, nil
}

func ensurePinnedAspect() (string, error) {
	root, err := workspaceRoot()
	if err != nil {
		return "", err
	}
	version, sum, err := aspectPin(root)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://github.com/aspect-build/aspect-cli/releases/download/v%s/aspect-cli-x86_64-unknown-linux-musl", version)
	return ensurePinnedTool("aspect", version, url, sum, root+"/.aspect/version.axl (guardian-sha256)")
}

// ensurePinnedTool returns ~/.cache/guardian/tools/<tool>/<version>/<tool>,
// downloading it from url on first use. The cached file is re-hashed against
// the pinned sum on every run so the cache cannot drift from the pin;
// pinSource names where the missing sum would live when warning.
func ensurePinnedTool(tool, version, url, sum, pinSource string) (string, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return "", fmt.Errorf("%s: unsupported platform %s/%s (linux/amd64 is the only controller platform today)", tool, runtime.GOOS, runtime.GOARCH)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(cache, "guardian", "tools", tool, version, tool)

	if _, err := os.Stat(bin); err != nil {
		if err := downloadFile(url, bin); err != nil {
			return "", fmt.Errorf("%s %s: %w", tool, version, err)
		}
		fmt.Fprintf(os.Stderr, "guardian: downloaded %s %s\n", tool, version)
	}
	if sum == "" {
		fmt.Fprintf(os.Stderr, "guardian: warning: no sha256 pin in %s; running %s %s unverified\n", pinSource, tool, version)
		return bin, nil
	}
	got, err := fileSHA256(bin)
	if err != nil {
		return "", err
	}
	if got != sum {
		return "", fmt.Errorf("%s %s: sha256 mismatch: pinned %s, cached file %s (delete %s to re-download)", tool, version, sum, got, bin)
	}
	return bin, nil
}

func downloadFile(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
