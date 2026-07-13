package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	agentContext = "platform-agent@guardian-mgmt"
	agentUser    = "platform-agent"
	adminContext = "admin@guardian-mgmt"
	adminUser    = "admin@guardian-mgmt"
	clusterName  = "guardian-mgmt"
)

type config struct {
	Mode          string
	Tofu          string
	TofuRoot      string
	Kubectl       string
	Talm          string
	Talosctl      string
	Kubelogin     string
	CA            string
	Kubeconfig    string
	TalmRoot      string
	Talosconfig   string
	OIDCIssuer    string
	OIDCClientID  string
	OIDCCacheDir  string
	Endpoints     string
	Nodes         string
	KubeAPIServer string
	ProbeTimeout  time.Duration
}

type tofuNode struct {
	ServerID    string `json:"server_id"`
	Hostname    string `json:"hostname"`
	PublicIPv4  string `json:"public_ipv4"`
	PrivateIPv4 string `json:"private_ipv4"`
}

// accessCandidate keeps endpoint and target together.  In particular, the
// default plan can never accidentally combine a pool of endpoints with one
// unrelated fixed target.
type accessCandidate struct {
	Name          string
	TalosEndpoint string
	TalosTarget   string
	KubernetesAPI string
}

type commandOptions struct {
	Env map[string]string
}

type commandRunner interface {
	Run(context.Context, string, []string, commandOptions) error
	Output(context.Context, string, []string, commandOptions) ([]byte, error)
}

type execCommandRunner struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (r execCommandRunner) Run(ctx context.Context, bin string, args []string, opts commandOptions) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = r.stdin
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	cmd.Env = mergedEnv(opts.Env)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", filepath.Base(bin), err)
	}
	return nil
}

func (r execCommandRunner) Output(ctx context.Context, bin string, args []string, opts commandOptions) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = r.stdin
	cmd.Env = mergedEnv(opts.Env)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("%s failed: %w: %s", filepath.Base(bin), err, detail)
		}
		return nil, fmt.Errorf("%s failed: %w", filepath.Base(bin), err)
	}
	return out, nil
}

func mergedEnv(overrides map[string]string) []string {
	env := os.Environ()
	for key, value := range overrides {
		prefix := key + "="
		for i := len(env) - 1; i >= 0; i-- {
			if strings.HasPrefix(env[i], prefix) {
				env = append(env[:i], env[i+1:]...)
			}
		}
		env = append(env, prefix+value)
	}
	return env
}

type fileSystem interface {
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, os.FileMode) error
	MkdirAll(string, os.FileMode) error
	TempFile(string, string) (string, error)
	Rename(string, string) error
	Remove(string) error
	Chmod(string, os.FileMode) error
	Stat(string) (os.FileInfo, error)
	Lstat(string) (os.FileInfo, error)
	EvalSymlinks(string) (string, error)
}

type osFileSystem struct{}

func (osFileSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (osFileSystem) WriteFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}
func (osFileSystem) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func (osFileSystem) TempFile(dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
func (osFileSystem) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
func (osFileSystem) Remove(path string) error             { return os.Remove(path) }
func (osFileSystem) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
func (osFileSystem) Stat(path string) (os.FileInfo, error)  { return os.Stat(path) }
func (osFileSystem) Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }
func (osFileSystem) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

type apiProbe func(context.Context, string, string, time.Duration, fileSystem) error

type application struct {
	cfg    config
	runner commandRunner
	fs     fileSystem
	probe  apiProbe
	stderr io.Writer
	now    func() time.Time
}

func main() {
	cfg := parseFlags()
	app := application{
		cfg: cfg,
		runner: execCommandRunner{
			stdin:  os.Stdin,
			stdout: os.Stdout,
			stderr: os.Stderr,
		},
		fs:     osFileSystem{},
		probe:  probeKubernetesTLS,
		stderr: os.Stderr,
		now:    time.Now,
	}
	if err := app.run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.Mode, "mode", "", "authentication mode: agent or admin")
	flag.StringVar(&cfg.Tofu, "tofu", "", "path to the OpenTofu binary")
	flag.StringVar(&cfg.TofuRoot, "tofu-root", "", "OpenTofu root containing guardian-mgmt state")
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Talm, "talm", "", "path to Talm (admin mode)")
	flag.StringVar(&cfg.Talosctl, "talosctl", "", "path to talosctl (admin mode)")
	flag.StringVar(&cfg.Kubelogin, "kubelogin", "", "path stored in the OIDC exec credential (agent mode)")
	flag.StringVar(&cfg.CA, "ca", "", "guardian-mgmt Kubernetes CA PEM")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "destination kubeconfig")
	flag.StringVar(&cfg.TalmRoot, "talm-root", "", "prepared Talm root (admin mode)")
	flag.StringVar(&cfg.Talosconfig, "talosconfig", "", "prepared talosconfig (admin mode)")
	flag.StringVar(&cfg.OIDCIssuer, "oidc-issuer", "", "OIDC issuer URL (agent mode)")
	flag.StringVar(&cfg.OIDCClientID, "oidc-client-id", "", "OIDC client ID (agent mode)")
	flag.StringVar(&cfg.OIDCCacheDir, "oidc-cache-dir", "", "kubelogin token cache directory (agent mode)")
	flag.StringVar(&cfg.Endpoints, "endpoints", "", "optional comma-separated Talos endpoints; requires --nodes")
	flag.StringVar(&cfg.Nodes, "nodes", "", "optional comma-separated Talos targets paired by index with --endpoints")
	flag.StringVar(&cfg.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API URL override")
	flag.DurationVar(&cfg.ProbeTimeout, "probe-timeout", 5*time.Second, "per-candidate Talos and Kubernetes TLS probe timeout")
	flag.Parse()
	return cfg
}

func (a *application) run(ctx context.Context) error {
	if err := validateConfig(a.cfg); err != nil {
		return err
	}
	resolvedKubeconfig, err := resolveKubeconfigPath(a.cfg.Kubeconfig, a.fs)
	if err != nil {
		return err
	}
	a.cfg.Kubeconfig = resolvedKubeconfig
	candidates, err := a.resolveCandidates(ctx)
	if err != nil {
		return err
	}
	if a.cfg.Mode == "agent" {
		return a.runAgent(ctx, candidates)
	}
	return a.runAdmin(ctx, candidates)
}

func resolveKubeconfigPath(path string, fs fileSystem) (string, error) {
	info, err := fs.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("inspect destination kubeconfig: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	resolved, err := fs.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve destination kubeconfig symlink %s: %w", path, err)
	}
	return resolved, nil
}

func validateConfig(cfg config) error {
	if cfg.Mode != "agent" && cfg.Mode != "admin" {
		return errors.New("--mode must be agent or admin")
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{"--kubectl", cfg.Kubectl},
		{"--ca", cfg.CA},
		{"--kubeconfig", cfg.Kubeconfig},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s is required", required.name)
		}
	}
	if cfg.ProbeTimeout <= 0 {
		return errors.New("--probe-timeout must be positive")
	}
	hasEndpoints := strings.TrimSpace(cfg.Endpoints) != ""
	hasNodes := strings.TrimSpace(cfg.Nodes) != ""
	if hasEndpoints != hasNodes {
		return errors.New("--endpoints and --nodes must be supplied together")
	}
	if cfg.Mode == "agent" && hasEndpoints {
		return errors.New("--endpoints and --nodes apply only to admin mode; use --kube-api-server for an agent override")
	}
	needsState := !hasEndpoints && !(cfg.Mode == "agent" && cfg.KubeAPIServer != "")
	if needsState {
		if cfg.Tofu == "" {
			return errors.New("--tofu is required when explicit endpoint/node pairs are not supplied")
		}
		if cfg.TofuRoot == "" {
			return errors.New("--tofu-root is required when explicit endpoint/node pairs are not supplied")
		}
	}
	if cfg.KubeAPIServer != "" {
		if err := validateKubernetesAPI(cfg.KubeAPIServer); err != nil {
			return fmt.Errorf("--kube-api-server: %w", err)
		}
	}
	if cfg.Mode == "agent" {
		for _, required := range []struct {
			name  string
			value string
		}{
			{"--kubelogin", cfg.Kubelogin},
			{"--oidc-issuer", cfg.OIDCIssuer},
			{"--oidc-client-id", cfg.OIDCClientID},
			{"--oidc-cache-dir", cfg.OIDCCacheDir},
		} {
			if strings.TrimSpace(required.value) == "" {
				return fmt.Errorf("%s is required in agent mode", required.name)
			}
		}
		return nil
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{"--talm", cfg.Talm},
		{"--talosctl", cfg.Talosctl},
		{"--talm-root", cfg.TalmRoot},
		{"--talosconfig", cfg.Talosconfig},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s is required in admin mode", required.name)
		}
	}
	return nil
}

func (a *application) resolveCandidates(ctx context.Context) ([]accessCandidate, error) {
	var candidates []accessCandidate
	var err error
	if strings.TrimSpace(a.cfg.Endpoints) != "" || strings.TrimSpace(a.cfg.Nodes) != "" {
		candidates, err = candidatesFromOverrides(a.cfg.Endpoints, a.cfg.Nodes)
	} else if a.cfg.Mode == "agent" && a.cfg.KubeAPIServer != "" {
		candidates = []accessCandidate{{Name: "kube-api-override", KubernetesAPI: a.cfg.KubeAPIServer}}
	} else {
		candidates, err = a.candidatesFromTofu(ctx)
	}
	if err != nil {
		return nil, err
	}
	if a.cfg.KubeAPIServer != "" {
		for i := range candidates {
			candidates[i].KubernetesAPI = a.cfg.KubeAPIServer
		}
	}
	return candidates, nil
}

func (a *application) candidatesFromTofu(ctx context.Context) ([]accessCandidate, error) {
	args := []string{"-chdir=" + a.cfg.TofuRoot, "output", "-json", "control_plane_nodes"}
	raw, err := a.runner.Output(ctx, a.cfg.Tofu, args, commandOptions{})
	if err != nil {
		return nil, fmt.Errorf("read OpenTofu control_plane_nodes: %w", err)
	}
	return parseControlPlaneNodes(raw)
}

func parseControlPlaneNodes(raw []byte) ([]accessCandidate, error) {
	var nodes map[string]tofuNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, fmt.Errorf("parse OpenTofu control_plane_nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, errors.New("OpenTofu control_plane_nodes is empty")
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := make(map[string]string, len(nodes))
	candidates := make([]accessCandidate, 0, len(nodes))
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("OpenTofu control_plane_nodes contains an empty node name")
		}
		node := nodes[name]
		ip := net.ParseIP(strings.TrimSpace(node.PublicIPv4))
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("OpenTofu node %q has invalid public_ipv4 %q", name, node.PublicIPv4)
		}
		publicIP := ip.To4().String()
		if previous, ok := seen[publicIP]; ok {
			return nil, fmt.Errorf("OpenTofu nodes %q and %q have duplicate public_ipv4 %s", previous, name, publicIP)
		}
		seen[publicIP] = name
		candidates = append(candidates, accessCandidate{
			Name:          name,
			TalosEndpoint: publicIP,
			TalosTarget:   publicIP,
			KubernetesAPI: apiURLForHost(publicIP),
		})
	}
	return candidates, nil
}

func candidatesFromOverrides(endpointCSV, nodeCSV string) ([]accessCandidate, error) {
	endpoints, err := strictCSV(endpointCSV)
	if err != nil {
		return nil, fmt.Errorf("--endpoints: %w", err)
	}
	nodes, err := strictCSV(nodeCSV)
	if err != nil {
		return nil, fmt.Errorf("--nodes: %w", err)
	}
	if len(endpoints) == 0 || len(nodes) == 0 {
		return nil, errors.New("--endpoints and --nodes must both contain at least one value")
	}
	if len(endpoints) != len(nodes) {
		return nil, fmt.Errorf("--endpoints has %d values but --nodes has %d; values must be paired by index", len(endpoints), len(nodes))
	}
	candidates := make([]accessCandidate, 0, len(endpoints))
	for i := range endpoints {
		if err := validateAddress(endpoints[i]); err != nil {
			return nil, fmt.Errorf("--endpoints value %d: %w", i+1, err)
		}
		if err := validateAddress(nodes[i]); err != nil {
			return nil, fmt.Errorf("--nodes value %d: %w", i+1, err)
		}
		candidates = append(candidates, accessCandidate{
			Name:          fmt.Sprintf("override-%d", i+1),
			TalosEndpoint: endpoints[i],
			TalosTarget:   nodes[i],
			KubernetesAPI: apiURLForHost(endpoints[i]),
		})
	}
	return candidates, nil
}

func strictCSV(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("value %d is empty", i+1)
		}
		out = append(out, part)
	}
	return out, nil
}

func validateAddress(address string) error {
	if net.ParseIP(address) != nil {
		return nil
	}
	if strings.ContainsAny(address, "/:@[]") || !strings.Contains(address, ".") {
		return fmt.Errorf("%q is not an IP address or DNS name", address)
	}
	for _, label := range strings.Split(address, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("%q is not an IP address or DNS name", address)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("%q is not an IP address or DNS name", address)
			}
		}
	}
	return nil
}

func apiURLForHost(host string) string {
	return "https://" + net.JoinHostPort(host, "6443")
}

func validateKubernetesAPI(server string) error {
	u, err := url.Parse(server)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("%q must be an https URL with a host", server)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q must not contain credentials, a query, or a fragment", server)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%q must not contain an API path", server)
	}
	return nil
}

func (a *application) runAgent(ctx context.Context, candidates []accessCandidate) error {
	candidate, err := a.selectAgentCandidate(ctx, candidates)
	if err != nil {
		return err
	}
	stage, err := a.stageDestination()
	if err != nil {
		return err
	}
	defer a.remove(stage)
	if err := a.fs.MkdirAll(a.cfg.OIDCCacheDir, 0o700); err != nil {
		return fmt.Errorf("create OIDC cache directory: %w", err)
	}

	kube := func(args ...string) error {
		return a.runner.Run(ctx, a.cfg.Kubectl, append([]string{"--kubeconfig", stage}, args...), commandOptions{})
	}
	steps := [][]string{
		{"config", "unset", "users." + agentUser},
		{"config", "set-cluster", clusterName, "--server=" + candidate.KubernetesAPI, "--certificate-authority=" + a.cfg.CA, "--embed-certs=true"},
		{
			"config", "set-credentials", agentUser,
			"--exec-api-version=client.authentication.k8s.io/v1beta1",
			"--exec-command=" + a.cfg.Kubelogin,
			"--exec-arg=get-token",
			"--exec-arg=--oidc-issuer-url=" + a.cfg.OIDCIssuer,
			"--exec-arg=--oidc-client-id=" + a.cfg.OIDCClientID,
			"--exec-arg=--grant-type=device-code",
			"--exec-arg=--oidc-extra-scope=offline_access",
			"--exec-arg=--token-cache-dir=" + a.cfg.OIDCCacheDir,
		},
		{"config", "set-context", agentContext, "--cluster=" + clusterName, "--user=" + agentUser},
		{"config", "use-context", agentContext},
	}
	for _, args := range steps {
		if err := kube(args...); err != nil {
			return fmt.Errorf("prepare platform-agent kubeconfig: %w", err)
		}
	}
	fmt.Fprintf(a.stderr, "verifying %s through %s; the first login may print a device-code URL\n", agentContext, candidate.KubernetesAPI)
	if err := kube("auth", "whoami"); err != nil {
		return fmt.Errorf("verify platform-agent credentials: %w", err)
	}
	// Breakglass material is removed only after the replacement daily-driver
	// credentials have successfully authenticated.
	for _, args := range [][]string{
		{"config", "unset", "users." + adminUser},
		{"config", "unset", "contexts." + adminContext},
	} {
		if err := kube(args...); err != nil {
			return fmt.Errorf("remove breakglass credentials from staged kubeconfig: %w", err)
		}
	}
	if err := a.install(stage); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "selected %s in %s\n", agentContext, a.cfg.Kubeconfig)
	return nil
}

func (a *application) selectAgentCandidate(ctx context.Context, candidates []accessCandidate) (accessCandidate, error) {
	var failures []string
	for _, candidate := range candidates {
		probeCtx, cancel := context.WithTimeout(ctx, a.cfg.ProbeTimeout)
		err := a.probe(probeCtx, candidate.KubernetesAPI, a.cfg.CA, a.cfg.ProbeTimeout, a.fs)
		cancel()
		if err == nil {
			return candidate, nil
		}
		failures = append(failures, fmt.Sprintf("%s (%s): %v", candidate.Name, candidate.KubernetesAPI, err))
		if ctx.Err() != nil {
			break
		}
	}
	return accessCandidate{}, fmt.Errorf("no reachable Kubernetes API candidate; attempts: %s", strings.Join(failures, "; "))
}

func (a *application) runAdmin(ctx context.Context, candidates []accessCandidate) error {
	if err := a.runner.Run(ctx, a.cfg.Talm, []string{
		"talosconfig",
		"--root", a.cfg.TalmRoot,
		"--talosconfig", a.cfg.Talosconfig,
	}, commandOptions{}); err != nil {
		return fmt.Errorf("prepare Talos client configuration: %w", err)
	}
	candidate, err := a.selectAdminCandidate(ctx, candidates)
	if err != nil {
		return err
	}
	minted := filepath.Join(a.cfg.TalmRoot, "kubeconfig")
	if err := a.fs.Remove(minted); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale Talm kubeconfig: %w", err)
	}
	defer a.remove(minted)
	talmArgs := []string{
		"kubeconfig",
		"--root", a.cfg.TalmRoot,
		"--talosconfig", a.cfg.Talosconfig,
		"--nodes", candidate.TalosTarget,
		"--endpoints", candidate.TalosEndpoint,
		"--merge=false",
		"--force",
	}
	if err := a.runner.Run(ctx, a.cfg.Talm, talmArgs, commandOptions{}); err != nil {
		return fmt.Errorf("mint admin kubeconfig through %s: %w", candidate.Name, err)
	}
	mintedRaw, err := a.fs.ReadFile(minted)
	if err != nil {
		return fmt.Errorf("read minted admin kubeconfig: %w", err)
	}
	if err := a.fs.Remove(minted); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove minted admin kubeconfig after staging: %w", err)
	}
	mintedStage, err := a.newTemp()
	if err != nil {
		return err
	}
	defer a.remove(mintedStage)
	if err := a.fs.WriteFile(mintedStage, mintedRaw, 0o600); err != nil {
		return fmt.Errorf("stage minted admin kubeconfig: %w", err)
	}
	kubeMinted := func(args ...string) error {
		return a.runner.Run(ctx, a.cfg.Kubectl, append([]string{"--kubeconfig", mintedStage}, args...), commandOptions{})
	}
	if err := kubeMinted("config", "use-context", adminContext); err != nil {
		return fmt.Errorf("select minted admin context: %w", err)
	}
	mintedClusterRaw, err := a.runner.Output(ctx, a.cfg.Kubectl, []string{
		"--kubeconfig", mintedStage,
		"config", "view", "--minify", "--output=jsonpath={.clusters[0].name}",
	}, commandOptions{})
	if err != nil {
		return fmt.Errorf("inspect minted admin context cluster: %w", err)
	}
	mintedCluster := strings.TrimSpace(string(mintedClusterRaw))
	if mintedCluster == "" {
		return errors.New("minted admin context has no cluster")
	}
	if err := kubeMinted("config", "set-cluster", mintedCluster, "--server="+candidate.KubernetesAPI); err != nil {
		return fmt.Errorf("set reachable API on minted admin kubeconfig: %w", err)
	}
	if err := kubeMinted("auth", "whoami"); err != nil {
		return fmt.Errorf("verify minted admin credentials: %w", err)
	}

	kubeconfigs := mintedStage
	if _, err := a.fs.Stat(a.cfg.Kubeconfig); err == nil {
		kubeconfigs += string(os.PathListSeparator) + a.cfg.Kubeconfig
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination kubeconfig: %w", err)
	}
	merged, err := a.runner.Output(ctx, a.cfg.Kubectl, []string{"config", "view", "--flatten", "--raw"}, commandOptions{
		Env: map[string]string{"KUBECONFIG": kubeconfigs},
	})
	if err != nil {
		return fmt.Errorf("merge admin kubeconfig: %w", err)
	}
	if len(strings.TrimSpace(string(merged))) == 0 {
		return errors.New("merge admin kubeconfig produced empty output")
	}
	installStage, err := a.newTemp()
	if err != nil {
		return err
	}
	defer a.remove(installStage)
	if err := a.fs.WriteFile(installStage, merged, 0o600); err != nil {
		return fmt.Errorf("stage merged admin kubeconfig: %w", err)
	}
	if err := a.runner.Run(ctx, a.cfg.Kubectl, []string{"--kubeconfig", installStage, "config", "use-context", adminContext}, commandOptions{}); err != nil {
		return fmt.Errorf("select admin context in merged kubeconfig: %w", err)
	}
	backup, err := a.backupDestination()
	if err != nil {
		return err
	}
	if err := a.install(installStage); err != nil {
		return err
	}
	if backup == "" {
		fmt.Fprintf(a.stderr, "selected %s in %s\n", adminContext, a.cfg.Kubeconfig)
	} else {
		fmt.Fprintf(a.stderr, "selected %s in %s (backup: %s)\n", adminContext, a.cfg.Kubeconfig, backup)
	}
	return nil
}

func (a *application) selectAdminCandidate(ctx context.Context, candidates []accessCandidate) (accessCandidate, error) {
	var failures []string
	for _, candidate := range candidates {
		probeCtx, cancel := context.WithTimeout(ctx, a.cfg.ProbeTimeout)
		err := a.runner.Run(probeCtx, a.cfg.Talosctl, []string{
			"--talosconfig", a.cfg.Talosconfig,
			"--endpoints", candidate.TalosEndpoint,
			"--nodes", candidate.TalosTarget,
			"version",
		}, commandOptions{})
		cancel()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s Talos endpoint=%s target=%s: %v", candidate.Name, candidate.TalosEndpoint, candidate.TalosTarget, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		probeCtx, cancel = context.WithTimeout(ctx, a.cfg.ProbeTimeout)
		err = a.probe(probeCtx, candidate.KubernetesAPI, a.cfg.CA, a.cfg.ProbeTimeout, a.fs)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s Kubernetes API=%s: %v", candidate.Name, candidate.KubernetesAPI, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		return candidate, nil
	}
	return accessCandidate{}, fmt.Errorf("no reachable admin access candidate; attempts: %s", strings.Join(failures, "; "))
}

func (a *application) stageDestination() (string, error) {
	stage, err := a.newTemp()
	if err != nil {
		return "", err
	}
	data := []byte("apiVersion: v1\nkind: Config\npreferences: {}\nclusters: []\nusers: []\ncontexts: []\ncurrent-context: \"\"\n")
	if _, err := a.fs.Stat(a.cfg.Kubeconfig); err == nil {
		data, err = a.fs.ReadFile(a.cfg.Kubeconfig)
		if err != nil {
			a.remove(stage)
			return "", fmt.Errorf("read destination kubeconfig: %w", err)
		}
	} else if !os.IsNotExist(err) {
		a.remove(stage)
		return "", fmt.Errorf("inspect destination kubeconfig: %w", err)
	}
	if err := a.fs.WriteFile(stage, data, 0o600); err != nil {
		a.remove(stage)
		return "", fmt.Errorf("stage destination kubeconfig: %w", err)
	}
	return stage, nil
}

func (a *application) newTemp() (string, error) {
	dir := filepath.Dir(a.cfg.Kubeconfig)
	if err := a.fs.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create kubeconfig directory: %w", err)
	}
	path, err := a.fs.TempFile(dir, ".guardian-auth-*")
	if err != nil {
		return "", fmt.Errorf("create staged kubeconfig: %w", err)
	}
	if err := a.fs.Chmod(path, 0o600); err != nil {
		a.remove(path)
		return "", fmt.Errorf("protect staged kubeconfig: %w", err)
	}
	return path, nil
}

func (a *application) install(stage string) error {
	if err := a.fs.Chmod(stage, 0o600); err != nil {
		return fmt.Errorf("protect staged kubeconfig: %w", err)
	}
	if err := a.fs.Rename(stage, a.cfg.Kubeconfig); err != nil {
		return fmt.Errorf("atomically install kubeconfig: %w", err)
	}
	return nil
}

func (a *application) backupDestination() (string, error) {
	if _, err := a.fs.Stat(a.cfg.Kubeconfig); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("inspect destination kubeconfig for backup: %w", err)
	}
	data, err := a.fs.ReadFile(a.cfg.Kubeconfig)
	if err != nil {
		return "", fmt.Errorf("read destination kubeconfig for backup: %w", err)
	}
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	backup := a.cfg.Kubeconfig + ".backup-" + now().UTC().Format("20060102150405")
	stage, err := a.newTemp()
	if err != nil {
		return "", err
	}
	defer a.remove(stage)
	if err := a.fs.WriteFile(stage, data, 0o600); err != nil {
		return "", fmt.Errorf("stage destination kubeconfig backup: %w", err)
	}
	if err := a.fs.Rename(stage, backup); err != nil {
		return "", fmt.Errorf("install destination kubeconfig backup: %w", err)
	}
	return backup, nil
}

func (a *application) remove(path string) {
	if path == "" {
		return
	}
	if err := a.fs.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(a.stderr, "WARN: remove temporary kubeconfig %s: %v\n", path, err)
	}
}

func probeKubernetesTLS(ctx context.Context, server, caPath string, timeout time.Duration, fs fileSystem) error {
	if err := validateKubernetesAPI(server); err != nil {
		return err
	}
	u, _ := url.Parse(server)
	caPEM, err := fs.ReadFile(caPath)
	if err != nil {
		return fmt.Errorf("read cluster CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("cluster CA contains no PEM certificates")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: u.Hostname(),
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return fmt.Errorf("TLS handshake: %w", err)
	}
	return conn.Close()
}
