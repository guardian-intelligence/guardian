package tests

import (
	"regexp"
	"strings"
	"testing"
)

// The kubeconfig records the absolute path of the kubelogin credential
// plugin, so that file has to outlive the workspace that minted it. Moving it
// under HOME is only half the job: a symlink into the minting workspace's
// Bazel output_base dies with that output_base — Bazel derives it from a hash
// of the workspace path, so a worktree owns one and takes the target with it,
// and `bazel clean --expunge` or external-repo eviction do the same to any
// workspace. The dangling link still lists in the directory, so the operator
// sees kubectl fail with `fork/exec ...: no such file or directory` for a file
// that is plainly there, and every cluster read stays broken until someone
// re-runs `aspect infra auth`. Copy, never link.
func TestStandingKubeloginShimIsCopiedNotLinked(t *testing.T) {
	infra := readText(t, runfilePath(".aspect/tasks/infra.axl"))

	call := regexp.MustCompile(`(?m)^\s*bin_dir = (\w+)\(ctx,[^)]*KUBELOGIN_TARGET[^)]*\)`).
		FindStringSubmatch(infra)
	if call == nil {
		t.Fatal("infra.axl no longer installs KUBELOGIN_TARGET into a bin_dir; the standing credential plugin is what this guards")
	}
	if call[1] != "install_standalone_tool_shim" {
		t.Fatalf("infra.axl installs the standing kubelogin shim with %s; it must use install_standalone_tool_shim, which copies the binary — a symlink dies with the minting workspace's output_base and strands cluster access", call[1])
	}
	if !strings.Contains(infra, `install_standalone_tool_shim(ctx, "kubectl-oidc_login", KUBELOGIN_TARGET, home + "/.guardian/tools/bin")`) {
		t.Error("the standing kubelogin shim must land under $HOME/.guardian/tools/bin: a workspace-relative path dies with its worktree")
	}

	helpers := readText(t, runfilePath(".aspect/lib/helpers.axl"))
	body := helperBody(t, helpers, "install_standalone_tool_shim")
	if strings.Contains(body, `"ln"`) {
		t.Error("install_standalone_tool_shim symlinks; it exists precisely to avoid that")
	}
	if !strings.Contains(body, `"install"`) {
		t.Error("install_standalone_tool_shim must copy the resolved binary (install -m 0755), so the shim does not depend on the Bazel output_base surviving")
	}
}

// An ordinary `aspect tools install` is what a developer reaches for when
// cluster access breaks, so it repairs an already-installed standing shim.
// It must not create one: minting a credential plugin is `aspect infra auth`'s
// call. The guard tests the directory rather than the shim, because an
// exists() that resolves through a symlink reports the broken shim as absent
// and would skip the one case worth healing.
func TestToolsInstallRepairsButNeverMintsTheStandingShim(t *testing.T) {
	tools := readText(t, runfilePath(".aspect/tasks/tools.axl"))
	if !strings.Contains(tools, `refresh_standalone_tool_shim(ctx, "kubectl-oidc_login"`) {
		t.Fatal("aspect tools install no longer refreshes the standing kubelogin shim; a wiped output_base then breaks cluster access until someone re-runs aspect infra auth")
	}

	helpers := readText(t, runfilePath(".aspect/lib/helpers.axl"))
	body := helperBody(t, helpers, "refresh_standalone_tool_shim")
	if !strings.Contains(body, "ctx.std.fs.exists(out_dir)") {
		t.Error("refresh_standalone_tool_shim must gate on the directory existing, not the shim: exists() on a dangling symlink reports absent and skips the repair")
	}
	if strings.Contains(body, `ctx.std.fs.exists(out_dir + "/" + name)`) {
		t.Error("gating on the shim path resolves through the symlink, so the dangling shim this is meant to repair is read as absent")
	}
}

// The name the repair passes must resolve to the same Bazel target the auth
// path installs, or `aspect tools install` would quietly overwrite the
// credential plugin with a different tool.
func TestStandingShimTargetAgreesAcrossTasks(t *testing.T) {
	infra := readText(t, runfilePath(".aspect/tasks/infra.axl"))
	tools := readText(t, runfilePath(".aspect/tasks/tools.axl"))

	m := regexp.MustCompile(`KUBELOGIN_TARGET = "([^"]+)"`).FindStringSubmatch(infra)
	if m == nil {
		t.Fatal("infra.axl no longer defines KUBELOGIN_TARGET")
	}
	want := m[1]

	entry := regexp.MustCompile(`\("kubectl-oidc_login", "([^"]+)"\)`).FindStringSubmatch(tools)
	if entry == nil {
		t.Fatal("tools.axl has no kubectl-oidc_login entry in DEBUG_CLI_SHIMS; the repair derives its target from that table")
	}
	if entry[1] != want {
		t.Fatalf("infra.axl installs %s as the credential plugin but tools.axl's shim table maps kubectl-oidc_login to %s: the repair would overwrite it with a different binary", want, entry[1])
	}
}

// helperBody returns the source of a single top-level def in an AXL file.
func helperBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "\ndef "+name+"(")
	if start < 0 {
		t.Fatalf("helpers.axl no longer defines %s", name)
	}
	rest := src[start+1:]
	if next := regexp.MustCompile(`\ndef `).FindStringIndex(rest); next != nil {
		return rest[:next[0]]
	}
	return rest
}
