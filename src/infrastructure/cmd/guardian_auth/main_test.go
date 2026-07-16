package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

type recordedCall struct {
	kind string
	bin  string
	args []string
	env  map[string]string
}

type fakeRunner struct {
	calls    []recordedCall
	runFn    func(context.Context, string, []string, commandOptions) error
	outputFn func(context.Context, string, []string, commandOptions) ([]byte, error)
}

type whoamiInterceptRunner struct {
	real commandRunner
}

type namedConfigEntry struct {
	Name string `json:"name"`
}

func (r whoamiInterceptRunner) Run(ctx context.Context, bin string, args []string, opts commandOptions) error {
	if hasArgSequence(args, "auth", "whoami") {
		return nil
	}
	return r.real.Run(ctx, bin, args, opts)
}

func (r whoamiInterceptRunner) Output(ctx context.Context, bin string, args []string, opts commandOptions) ([]byte, error) {
	return r.real.Output(ctx, bin, args, opts)
}

func (r *fakeRunner) Run(ctx context.Context, bin string, args []string, opts commandOptions) error {
	r.calls = append(r.calls, recordedCall{kind: "run", bin: bin, args: append([]string(nil), args...), env: cloneMap(opts.Env)})
	if r.runFn != nil {
		return r.runFn(ctx, bin, args, opts)
	}
	return nil
}

func (r *fakeRunner) Output(ctx context.Context, bin string, args []string, opts commandOptions) ([]byte, error) {
	r.calls = append(r.calls, recordedCall{kind: "output", bin: bin, args: append([]string(nil), args...), env: cloneMap(opts.Env)})
	if r.outputFn != nil {
		return r.outputFn(ctx, bin, args, opts)
	}
	return nil, nil
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func successfulProbe(context.Context, string, string, time.Duration, fileSystem) error { return nil }

func TestResolveCandidatesDefaultsToStableEndpoint(t *testing.T) {
	agent := baseAgentConfig(t.TempDir())
	got, err := resolveCandidates(agent)
	if err != nil {
		t.Fatalf("resolveCandidates() error = %v", err)
	}
	want := []accessCandidate{{Name: clusterName, KubernetesAPI: defaultKubeAPIServer}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCandidates(agent) = %#v, want %#v", got, want)
	}

	admin := baseAdminConfig(t.TempDir())
	got, err = resolveCandidates(admin)
	if err != nil {
		t.Fatalf("resolveCandidates() error = %v", err)
	}
	want = []accessCandidate{{
		Name:          clusterName,
		TalosEndpoint: defaultTalosEndpoint,
		TalosTarget:   defaultTalosEndpoint,
		KubernetesAPI: defaultKubeAPIServer,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCandidates(admin) = %#v, want %#v", got, want)
	}
}

func TestResolveCandidatesOverridePairsCarryConfiguredAPI(t *testing.T) {
	cfg := baseAdminConfig(t.TempDir())
	cfg.Endpoints = "203.0.113.1,203.0.113.2"
	cfg.Nodes = "10.8.0.11,10.8.0.12"
	cfg.KubeAPIServer = "https://203.0.113.9:6443"
	got, err := resolveCandidates(cfg)
	if err != nil {
		t.Fatalf("resolveCandidates() error = %v", err)
	}
	want := []accessCandidate{
		{Name: "override-1", TalosEndpoint: "203.0.113.1", TalosTarget: "10.8.0.11", KubernetesAPI: "https://203.0.113.9:6443"},
		{Name: "override-2", TalosEndpoint: "203.0.113.2", TalosTarget: "10.8.0.12", KubernetesAPI: "https://203.0.113.9:6443"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCandidates() = %#v, want %#v", got, want)
	}
}

func TestCandidatesFromOverridesPairsByIndex(t *testing.T) {
	got, err := candidatesFromOverrides("203.0.113.1,203.0.113.2", "10.8.0.11,10.8.0.12")
	if err != nil {
		t.Fatalf("candidatesFromOverrides() error = %v", err)
	}
	want := []accessCandidate{
		{Name: "override-1", TalosEndpoint: "203.0.113.1", TalosTarget: "10.8.0.11"},
		{Name: "override-2", TalosEndpoint: "203.0.113.2", TalosTarget: "10.8.0.12"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidatesFromOverrides() = %#v, want %#v", got, want)
	}
}

func TestCandidatesFromOverridesRejectsAmbiguousLists(t *testing.T) {
	for _, tc := range []struct {
		endpoints string
		nodes     string
		want      string
	}{
		{"203.0.113.1,203.0.113.2", "10.8.0.11", "paired by index"},
		{"203.0.113.1,", "10.8.0.11,10.8.0.12", "is empty"},
		{"bad host", "10.8.0.11", "not an IP address or DNS name"},
	} {
		_, err := candidatesFromOverrides(tc.endpoints, tc.nodes)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("candidatesFromOverrides(%q, %q) error = %v, want %q", tc.endpoints, tc.nodes, err, tc.want)
		}
	}
}

func TestValidateConfigRequiresPairedOverrides(t *testing.T) {
	cfg := baseAgentConfig(t.TempDir())
	cfg.Endpoints = "203.0.113.1"
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "supplied together") {
		t.Fatalf("validateConfig() error = %v, want paired-override error", err)
	}
}

func TestValidateConfigRejectsTalosOverridesInAgentMode(t *testing.T) {
	cfg := baseAgentConfig(t.TempDir())
	cfg.Endpoints = "203.0.113.1"
	cfg.Nodes = "203.0.113.1"
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "only to admin mode") {
		t.Fatalf("validateConfig() error = %v, want admin-only override error", err)
	}
}

func TestValidateConfigRejectsEmptyKubeAPIServer(t *testing.T) {
	cfg := baseAgentConfig(t.TempDir())
	cfg.KubeAPIServer = ""
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "--kube-api-server") {
		t.Fatalf("validateConfig() error = %v, want kube-api-server rejection", err)
	}
}

func TestValidateKubernetesAPIRejectsPath(t *testing.T) {
	if err := validateKubernetesAPI("https://api.guardian.example:6443/not-the-api-root"); err == nil || !strings.Contains(err.Error(), "API path") {
		t.Fatalf("validateKubernetesAPI() error = %v, want path rejection", err)
	}
}

func TestSelectAgentCandidateFailsOver(t *testing.T) {
	var probed []string
	app := application{
		cfg: config{CA: "/ca", ProbeTimeout: time.Second},
		probe: func(_ context.Context, server, _ string, _ time.Duration, _ fileSystem) error {
			probed = append(probed, server)
			if strings.Contains(server, ".1:") {
				return errors.New("down")
			}
			return nil
		},
	}
	candidates := defaultCandidates()
	got, err := app.selectAgentCandidate(context.Background(), candidates)
	if err != nil {
		t.Fatalf("selectAgentCandidate() error = %v", err)
	}
	if got.Name != "wind" {
		t.Fatalf("selected %q, want wind", got.Name)
	}
	if !reflect.DeepEqual(probed, []string{candidates[0].KubernetesAPI, candidates[1].KubernetesAPI}) {
		t.Fatalf("probed = %#v", probed)
	}
}

func TestSelectAdminCandidateRequiresTalosAndKubernetes(t *testing.T) {
	runner := &fakeRunner{runFn: func(_ context.Context, bin string, args []string, _ commandOptions) error {
		if bin != "/talosctl" {
			t.Fatalf("unexpected binary %q", bin)
		}
		if valueAfter(args, "--endpoints") == "203.0.113.1" {
			return errors.New("Talos unavailable")
		}
		return nil
	}}
	var probed []string
	app := application{
		cfg:    config{Talosctl: "/talosctl", Talosconfig: "/talosconfig", CA: "/ca", ProbeTimeout: time.Second},
		runner: runner,
		probe: func(_ context.Context, server, _ string, _ time.Duration, _ fileSystem) error {
			probed = append(probed, server)
			return nil
		},
	}
	candidates := defaultCandidates()
	got, err := app.selectAdminCandidate(context.Background(), candidates)
	if err != nil {
		t.Fatalf("selectAdminCandidate() error = %v", err)
	}
	if got.Name != "wind" {
		t.Fatalf("selected %q, want wind", got.Name)
	}
	if !reflect.DeepEqual(probed, []string{candidates[1].KubernetesAPI}) {
		t.Fatalf("Kubernetes probes = %#v; a Talos failure must reject earth before TLS probing", probed)
	}
	for i, call := range runner.calls {
		candidate := candidates[i]
		if valueAfter(call.args, "--endpoints") != candidate.TalosEndpoint || valueAfter(call.args, "--nodes") != candidate.TalosTarget {
			t.Fatalf("Talos call %d split candidate: %#v", i, call)
		}
	}
}

func TestSelectAdminCandidateRejectsTalosSuccessWhenKubernetesFails(t *testing.T) {
	runner := &fakeRunner{}
	app := application{
		cfg:    config{Talosctl: "/talosctl", Talosconfig: "/talosconfig", CA: "/ca", ProbeTimeout: time.Second},
		runner: runner,
		probe: func(_ context.Context, server, _ string, _ time.Duration, _ fileSystem) error {
			if strings.Contains(server, ".1:") {
				return errors.New("wrong certificate")
			}
			return nil
		},
	}
	got, err := app.selectAdminCandidate(context.Background(), defaultCandidates())
	if err != nil {
		t.Fatalf("selectAdminCandidate() error = %v", err)
	}
	if got.Name != "wind" {
		t.Fatalf("selected %q, want wind", got.Name)
	}
}

func TestSelectAdminCandidateReportsEveryAttempt(t *testing.T) {
	runner := &fakeRunner{runFn: func(context.Context, string, []string, commandOptions) error {
		return errors.New("down")
	}}
	app := application{cfg: config{Talosctl: "/talosctl", Talosconfig: "/talosconfig", ProbeTimeout: time.Second}, runner: runner}
	_, err := app.selectAdminCandidate(context.Background(), defaultCandidates())
	if err == nil || !strings.Contains(err.Error(), "earth") || !strings.Contains(err.Error(), "wind") {
		t.Fatalf("selectAdminCandidate() error = %v, want every named attempt", err)
	}
}

func TestCanceledSelectionStopsAfterCurrentCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var probes int
	app := application{
		cfg: config{CA: "/ca", ProbeTimeout: time.Second},
		probe: func(ctx context.Context, _ string, _ string, _ time.Duration, _ fileSystem) error {
			probes++
			return ctx.Err()
		},
	}
	if _, err := app.selectAgentCandidate(ctx, defaultCandidates()); err == nil {
		t.Fatal("selectAgentCandidate() accepted canceled context")
	}
	if probes != 1 {
		t.Fatalf("probes = %d, want 1 after cancellation", probes)
	}
}

func TestProbeKubernetesTLSVerifiesConfiguredCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	ca := filepath.Join(t.TempDir(), "cluster-ca.crt")
	mustWrite(t, ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	if err := probeKubernetesTLS(context.Background(), server.URL, ca, time.Second, osFileSystem{}); err != nil {
		t.Fatalf("probeKubernetesTLS() error = %v", err)
	}

	wrongName := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	if err := probeKubernetesTLS(context.Background(), wrongName, ca, time.Second, osFileSystem{}); err == nil {
		t.Fatal("probeKubernetesTLS() accepted a certificate for the wrong server name")
	}
}

func TestRunAgentVerificationFailureLeavesDestinationUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAgentConfig(dir)
	original := []byte("unrelated-context: must-survive\n")
	mustWrite(t, cfg.Kubeconfig, original)
	runner := &fakeRunner{runFn: func(_ context.Context, _ string, args []string, _ commandOptions) error {
		if hasArgSequence(args, "auth", "whoami") {
			return errors.New("device login failed")
		}
		return nil
	}}
	app := testApplication(cfg, runner)
	err := app.runAgent(context.Background(), defaultCandidates())
	if err == nil || !strings.Contains(err.Error(), "verify platform-agent") {
		t.Fatalf("runAgent() error = %v", err)
	}
	assertFileEquals(t, cfg.Kubeconfig, original)
	for _, call := range runner.calls {
		if hasArgSequence(call.args, "config", "unset", "users."+adminUser) {
			t.Fatalf("breakglass user removed before successful whoami: %#v", call)
		}
	}
	assertNoAuthTemps(t, dir)
}

func TestRunAgentSuccessRemovesBreakglassAfterWhoamiAndInstallsAtomically(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAgentConfig(dir)
	original := []byte("apiVersion: v1\nunrelated-context: survives\n")
	mustWrite(t, cfg.Kubeconfig, original)
	runner := &fakeRunner{}
	app := testApplication(cfg, runner)
	if err := app.runAgent(context.Background(), defaultCandidates()); err != nil {
		t.Fatalf("runAgent() error = %v", err)
	}
	// The fake kubectl deliberately does not rewrite its input.  Exact survival
	// proves that the transaction was staged from, rather than instead of, the
	// operator's existing config.
	assertFileEquals(t, cfg.Kubeconfig, original)
	assertMode0600(t, cfg.Kubeconfig)
	whoami := callIndex(runner.calls, "auth", "whoami")
	removeAdmin := callIndex(runner.calls, "config", "unset", "users."+adminUser)
	if whoami < 0 || removeAdmin <= whoami {
		t.Fatalf("call order = %#v; breakglass cleanup must follow whoami", runner.calls)
	}
	assertNoAuthTemps(t, dir)
}

func TestRunAgentUsesKubectlToPreserveUnrelatedConfigAndRemoveBreakglass(t *testing.T) {
	kubectl := kubectlRunfile(t)
	dir := t.TempDir()
	cfg := baseAgentConfig(dir)
	cfg.Kubectl = kubectl
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cfg.CA = filepath.Join(dir, "cluster-ca.crt")
	mustWrite(t, cfg.CA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))

	mustWrite(t, cfg.Kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters:
- name: unrelated-cluster
  cluster:
    server: https://unrelated.invalid
- name: admin-cluster
  cluster:
    server: https://admin.invalid
users:
- name: unrelated-user
  user:
    token: unrelated
- name: admin@guardian-mgmt
  user:
    token: breakglass
contexts:
- name: unrelated
  context:
    cluster: unrelated-cluster
    user: unrelated-user
- name: admin@guardian-mgmt
  context:
    cluster: admin-cluster
    user: admin@guardian-mgmt
current-context: unrelated
`))
	real := execCommandRunner{stdin: strings.NewReader(""), stdout: ioDiscard{}, stderr: ioDiscard{}}
	app := application{
		cfg:    cfg,
		runner: whoamiInterceptRunner{real: real},
		fs:     osFileSystem{},
		probe:  successfulProbe,
		stderr: ioDiscard{},
	}
	if err := app.runAgent(context.Background(), []accessCandidate{{
		Name:          "earth",
		KubernetesAPI: server.URL,
	}}); err != nil {
		t.Fatalf("runAgent() with real kubectl error = %v", err)
	}

	raw, err := real.Output(context.Background(), kubectl, []string{
		"--kubeconfig", cfg.Kubeconfig, "config", "view", "-o", "json",
	}, commandOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		CurrentContext string             `json:"current-context"`
		Contexts       []namedConfigEntry `json:"contexts"`
		Users          []namedConfigEntry `json:"users"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("parse kubectl config view: %v", err)
	}
	if view.CurrentContext != agentContext {
		t.Fatalf("current context = %q, want %q", view.CurrentContext, agentContext)
	}
	contextNames := namesFromView(view.Contexts)
	userNames := namesFromView(view.Users)
	for _, name := range []string{"unrelated", agentContext} {
		if !contextNames[name] {
			t.Fatalf("contexts = %#v, missing %q", contextNames, name)
		}
	}
	if contextNames[adminContext] {
		t.Fatalf("contexts = %#v, breakglass context remains", contextNames)
	}
	for _, name := range []string{"unrelated-user", agentUser} {
		if !userNames[name] {
			t.Fatalf("users = %#v, missing %q", userNames, name)
		}
	}
	if userNames[adminUser] {
		t.Fatalf("users = %#v, breakglass user remains", userNames)
	}
}

func kubectlRunfile(t *testing.T) string {
	t.Helper()
	path, err := runfiles.Rlocation("multitool/tools/kubectl/kubectl")
	if err == nil {
		return path
	}
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Fatalf("locate pinned kubectl runfile: %v", err)
	}
	t.Skip("pinned kubectl is available under Bazel runfiles")
	return ""
}

func namesFromView(entries []namedConfigEntry) map[string]bool {
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name] = true
	}
	return names
}

type renameFailFS struct{ osFileSystem }

func (renameFailFS) Rename(string, string) error { return errors.New("injected rename failure") }

type nthRenameFailFS struct {
	osFileSystem
	failAt int
	calls  int
}

func (f *nthRenameFailFS) Rename(oldPath, newPath string) error {
	f.calls++
	if f.calls == f.failAt {
		return errors.New("injected rename failure")
	}
	return f.osFileSystem.Rename(oldPath, newPath)
}

func TestRunAgentFinalRenameFailureLeavesDestinationUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAgentConfig(dir)
	original := []byte("original\n")
	mustWrite(t, cfg.Kubeconfig, original)
	app := testApplication(cfg, &fakeRunner{})
	app.fs = renameFailFS{}
	err := app.runAgent(context.Background(), defaultCandidates())
	if err == nil || !strings.Contains(err.Error(), "atomically install") {
		t.Fatalf("runAgent() error = %v", err)
	}
	assertFileEquals(t, cfg.Kubeconfig, original)
	assertNoAuthTemps(t, dir)
}

func TestRunAgentPreservesDestinationSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed-kubeconfig")
	link := filepath.Join(dir, "config")
	original := []byte("apiVersion: v1\nunrelated-context: survives\n")
	mustWrite(t, target, original)
	if err := os.Symlink(filepath.Base(target), link); err != nil {
		t.Fatal(err)
	}
	cfg := baseAgentConfig(dir)
	cfg.Kubeconfig = link
	cfg.KubeAPIServer = "https://203.0.113.1:6443"
	app := testApplication(cfg, &fakeRunner{})
	if err := app.run(context.Background()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("destination %s is no longer a symlink", link)
	}
	assertFileEquals(t, target, original)
	assertMode0600(t, target)
}

func TestRunRejectsDanglingDestinationSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config")
	if err := os.Symlink("missing-kubeconfig", link); err != nil {
		t.Fatal(err)
	}
	cfg := baseAgentConfig(dir)
	cfg.Kubeconfig = link
	cfg.KubeAPIServer = "https://203.0.113.1:6443"
	runner := &fakeRunner{}
	app := testApplication(cfg, runner)
	if err := app.run(context.Background()); err == nil || !strings.Contains(err.Error(), "resolve destination kubeconfig symlink") {
		t.Fatalf("run() error = %v, want dangling-symlink error", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("commands ran before destination symlink validation: %#v", runner.calls)
	}
}

func TestRunAdminMintFailureLeavesDestinationUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAdminConfig(dir)
	original := []byte("original\n")
	mustWrite(t, cfg.Kubeconfig, original)
	runner := &fakeRunner{runFn: func(_ context.Context, bin string, args []string, _ commandOptions) error {
		if bin == cfg.Talm && len(args) > 0 && args[0] == "kubeconfig" {
			return errors.New("mint failed")
		}
		return nil
	}}
	app := testApplication(cfg, runner)
	err := app.runAdmin(context.Background(), defaultCandidates())
	if err == nil || !strings.Contains(err.Error(), "mint admin") {
		t.Fatalf("runAdmin() error = %v", err)
	}
	assertFileEquals(t, cfg.Kubeconfig, original)
}

func TestRunAdminKubeconfigMintFailureRemovesPartialCredentials(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAdminConfig(dir)
	original := []byte("original\n")
	mustWrite(t, cfg.Kubeconfig, original)
	minted := filepath.Join(cfg.TalmRoot, "kubeconfig")
	runner := &fakeRunner{runFn: func(_ context.Context, bin string, args []string, _ commandOptions) error {
		if bin == cfg.Talm && len(args) > 0 && args[0] == "kubeconfig" {
			mustWrite(t, minted, []byte("partial-admin-credentials\n"))
			return errors.New("mint interrupted")
		}
		return nil
	}}
	app := testApplication(cfg, runner)
	if err := app.runAdmin(context.Background(), defaultCandidates()); err == nil || !strings.Contains(err.Error(), "mint admin") {
		t.Fatalf("runAdmin() error = %v, want mint failure", err)
	}
	assertFileEquals(t, cfg.Kubeconfig, original)
	if _, err := os.Stat(minted); !os.IsNotExist(err) {
		t.Fatalf("partial minted kubeconfig remains after failure: %v", err)
	}
}

func TestRunAdminVerificationFailureLeavesDestinationUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAdminConfig(dir)
	original := []byte("original\n")
	mustWrite(t, cfg.Kubeconfig, original)
	runner := adminRunner(t, cfg, nil)
	runner.runFn = func(_ context.Context, bin string, args []string, _ commandOptions) error {
		if bin == cfg.Talm && len(args) > 0 && args[0] == "kubeconfig" {
			mustWrite(t, filepath.Join(cfg.TalmRoot, "kubeconfig"), []byte("minted\n"))
		}
		if hasArgSequence(args, "auth", "whoami") {
			return errors.New("minted cert rejected")
		}
		return nil
	}
	app := testApplication(cfg, runner)
	err := app.runAdmin(context.Background(), defaultCandidates())
	if err == nil || !strings.Contains(err.Error(), "verify minted admin") {
		t.Fatalf("runAdmin() error = %v", err)
	}
	assertFileEquals(t, cfg.Kubeconfig, original)
	if _, err := os.Stat(filepath.Join(cfg.TalmRoot, "kubeconfig")); !os.IsNotExist(err) {
		t.Fatalf("minted admin kubeconfig remains after failed verification: %v", err)
	}
	for _, call := range runner.calls {
		if call.kind == "output" && call.bin == cfg.Kubectl && hasArgSequence(call.args, "config", "view", "--flatten", "--raw") {
			t.Fatalf("merge ran after failed credential verification: %#v", call)
		}
	}
	assertNoAuthTemps(t, dir)
}

func TestRunAdminFailoverMintsExactPairVerifiesThenPreservesMerge(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAdminConfig(dir)
	mustWrite(t, cfg.Kubeconfig, []byte("existing-unrelated-context\n"))
	merged := []byte("apiVersion: v1\ncontexts:\n- name: unrelated\n- name: admin@guardian-mgmt\n")
	runner := adminRunner(t, cfg, merged)
	runner.runFn = func(_ context.Context, bin string, args []string, _ commandOptions) error {
		if bin == cfg.Talosctl && valueAfter(args, "--endpoints") == "203.0.113.1" {
			return errors.New("first node down")
		}
		if bin == cfg.Talm && len(args) > 0 && args[0] == "kubeconfig" {
			mustWrite(t, filepath.Join(cfg.TalmRoot, "kubeconfig"), []byte("minted-admin\n"))
		}
		return nil
	}
	app := testApplication(cfg, runner)
	if err := app.runAdmin(context.Background(), defaultCandidates()); err != nil {
		t.Fatalf("runAdmin() error = %v", err)
	}
	assertFileEquals(t, cfg.Kubeconfig, merged)
	assertMode0600(t, cfg.Kubeconfig)
	if _, err := os.Stat(filepath.Join(cfg.TalmRoot, "kubeconfig")); !os.IsNotExist(err) {
		t.Fatalf("minted admin kubeconfig remains after install: %v", err)
	}
	backup := cfg.Kubeconfig + ".backup-20250102030405"
	assertFileEquals(t, backup, []byte("existing-unrelated-context\n"))
	assertMode0600(t, backup)

	var talmCall *recordedCall
	var mergeCall *recordedCall
	for i := range runner.calls {
		call := &runner.calls[i]
		if call.bin == cfg.Talm {
			talmCall = call
		}
		if call.kind == "output" && call.bin == cfg.Kubectl {
			mergeCall = call
		}
	}
	if talmCall == nil {
		t.Fatal("Talm was not invoked")
	}
	if got := valueAfter(talmCall.args, "--endpoints"); got != "203.0.113.2" {
		t.Fatalf("Talm endpoint = %q, want selected wind endpoint", got)
	}
	if got := valueAfter(talmCall.args, "--nodes"); got != "203.0.113.2" {
		t.Fatalf("Talm target = %q, want the same selected wind target", got)
	}
	if mergeCall == nil || !strings.HasSuffix(mergeCall.env["KUBECONFIG"], string(os.PathListSeparator)+cfg.Kubeconfig) {
		t.Fatalf("merge call = %#v, want minted config first and existing destination second", mergeCall)
	}
	whoami := callIndex(runner.calls, "auth", "whoami")
	merge := outputCallIndex(runner.calls, cfg.Kubectl, "config", "view", "--flatten", "--raw")
	if whoami < 0 || merge <= whoami {
		t.Fatalf("call order = %#v; merge must follow minted credential verification", runner.calls)
	}
	assertNoAuthTemps(t, dir)
}

func TestRunAdminPreparesTalosconfigAndUsesMintedClusterName(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAdminConfig(dir)
	runner := adminRunner(t, cfg, []byte("merged\n"))
	runner.runFn = func(_ context.Context, bin string, args []string, _ commandOptions) error {
		if bin == cfg.Talm && len(args) > 0 && args[0] == "kubeconfig" {
			mustWrite(t, filepath.Join(cfg.TalmRoot, "kubeconfig"), []byte("minted\n"))
		}
		return nil
	}
	app := testApplication(cfg, runner)
	if err := app.runAdmin(context.Background(), defaultCandidates()[:1]); err != nil {
		t.Fatalf("runAdmin() error = %v", err)
	}
	talosconfig := -1
	talosProbe := -1
	setGeneratedCluster := -1
	for i, call := range runner.calls {
		if call.bin == cfg.Talm && hasArgSequence(call.args, "talosconfig", "--root", cfg.TalmRoot, "--talosconfig", cfg.Talosconfig) {
			talosconfig = i
		}
		if call.bin == cfg.Talosctl {
			talosProbe = i
		}
		if call.bin == cfg.Kubectl && hasArgSequence(call.args, "config", "set-cluster", "generated-cluster-key", "--server=https://203.0.113.1:6443") {
			setGeneratedCluster = i
		}
	}
	if talosconfig < 0 || talosProbe <= talosconfig {
		t.Fatalf("calls = %#v; talm talosconfig must precede talosctl probing", runner.calls)
	}
	if setGeneratedCluster < 0 {
		t.Fatalf("calls = %#v; admin flow did not update the minted context's actual cluster key", runner.calls)
	}
}

func TestRunAdminFinalRenameFailureLeavesDestinationUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := baseAdminConfig(dir)
	original := []byte("original\n")
	mustWrite(t, cfg.Kubeconfig, original)
	runner := adminRunner(t, cfg, []byte("merged-admin\n"))
	runner.runFn = func(_ context.Context, bin string, args []string, _ commandOptions) error {
		if bin == cfg.Talm && len(args) > 0 && args[0] == "kubeconfig" {
			mustWrite(t, filepath.Join(cfg.TalmRoot, "kubeconfig"), []byte("minted-admin\n"))
		}
		return nil
	}
	app := testApplication(cfg, runner)
	app.fs = &nthRenameFailFS{failAt: 2}
	if err := app.runAdmin(context.Background(), defaultCandidates()[:1]); err == nil || !strings.Contains(err.Error(), "atomically install") {
		t.Fatalf("runAdmin() error = %v, want final install failure", err)
	}
	assertFileEquals(t, cfg.Kubeconfig, original)
	assertFileEquals(t, cfg.Kubeconfig+".backup-20250102030405", original)
	assertNoAuthTemps(t, dir)
}

func baseAgentConfig(dir string) config {
	return config{
		Mode:          "agent",
		Kubectl:       "/tools/kubectl",
		Kubelogin:     "/tools/kubectl-oidc_login",
		CA:            "/repo/ca.crt",
		Kubeconfig:    filepath.Join(dir, "config"),
		OIDCIssuer:    "https://keycloak.example/realms/platform",
		OIDCClientID:  "guardian-platform-agent",
		OIDCCacheDir:  filepath.Join(dir, "oidc-cache"),
		KubeAPIServer: defaultKubeAPIServer,
		ProbeTimeout:  time.Second,
	}
}

func baseAdminConfig(dir string) config {
	talmRoot := filepath.Join(dir, "talm")
	_ = os.MkdirAll(talmRoot, 0o700)
	return config{
		Mode:          "admin",
		Kubectl:       "/tools/kubectl",
		Talm:          "/tools/talm",
		Talosctl:      "/tools/talosctl",
		CA:            "/repo/ca.crt",
		Kubeconfig:    filepath.Join(dir, "config"),
		TalmRoot:      talmRoot,
		Talosconfig:   filepath.Join(talmRoot, "talosconfig"),
		KubeAPIServer: defaultKubeAPIServer,
		ProbeTimeout:  time.Second,
	}
}

func testApplication(cfg config, runner *fakeRunner) application {
	return application{
		cfg:    cfg,
		runner: runner,
		fs:     osFileSystem{},
		probe:  successfulProbe,
		stderr: ioDiscard{},
		now: func() time.Time {
			return time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
		},
	}
}

// Avoid importing bytes just to provide a quiet writer in application tests.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func adminRunner(t *testing.T, cfg config, merged []byte) *fakeRunner {
	t.Helper()
	return &fakeRunner{outputFn: func(_ context.Context, bin string, args []string, _ commandOptions) ([]byte, error) {
		if bin == cfg.Kubectl && hasArgSequence(args, "config", "view", "--minify", "--output=jsonpath={.clusters[0].name}") {
			return []byte("generated-cluster-key"), nil
		}
		if bin != cfg.Kubectl || !reflect.DeepEqual(args, []string{"config", "view", "--flatten", "--raw"}) {
			return nil, fmt.Errorf("unexpected output: %s %#v", bin, args)
		}
		return merged, nil
	}}
}

func defaultCandidates() []accessCandidate {
	return []accessCandidate{
		{Name: "earth", TalosEndpoint: "203.0.113.1", TalosTarget: "203.0.113.1", KubernetesAPI: "https://203.0.113.1:6443"},
		{Name: "wind", TalosEndpoint: "203.0.113.2", TalosTarget: "203.0.113.2", KubernetesAPI: "https://203.0.113.2:6443"},
	}
}

func valueAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func hasArgSequence(args []string, want ...string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(args); i++ {
		if reflect.DeepEqual(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func callIndex(calls []recordedCall, want ...string) int {
	for i, call := range calls {
		if call.kind == "run" && hasArgSequence(call.args, want...) {
			return i
		}
	}
	return -1
}

func outputCallIndex(calls []recordedCall, bin string, want ...string) int {
	for i, call := range calls {
		if call.kind == "output" && call.bin == bin && hasArgSequence(call.args, want...) {
			return i
		}
	}
	return -1
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode = %o, want 600", path, got)
	}
}

func assertNoAuthTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".guardian-auth-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary kubeconfigs remain: %#v", matches)
	}
}
