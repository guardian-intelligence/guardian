package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type loadConfig struct {
	K6                   string
	Script               string
	Kubectl              string
	Kubeconfig           string
	RequestTimeout       string
	WaitTimeout          string
	Surface              string
	Stage                string
	URL                  string
	ExpectedStatuses     string
	HostOverrides        string
	VUs                  string
	Duration             string
	HTTPFailedThreshold  string
	SleepSeconds         string
	PortForwardReadyWait time.Duration
}

type targetSpec struct {
	URL              string
	ExpectedStatuses string
	RequestName      string
	NeedsOpenBaoPort bool
}

type kubectlCommand struct {
	Label string
	Args  []string
}

func main() {
	var cfg loadConfig
	var portForwardReadyWait string
	flag.StringVar(&cfg.K6, "k6", "", "path to k6")
	flag.StringVar(&cfg.Script, "script", "", "path to k6 script")
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl; required for OpenBao port-forward")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "15m", "timeout waiting for Kubernetes surface readiness")
	flag.StringVar(&cfg.Surface, "surface", "", "surface to load: harbor, dashboard, openbao, or custom with --url")
	flag.StringVar(&cfg.Stage, "stage", "root", "Guardian bootstrap stage: root")
	flag.StringVar(&cfg.URL, "url", "", "explicit URL to load; overrides --surface target mapping")
	flag.StringVar(&cfg.ExpectedStatuses, "expected-statuses", "", "comma-separated acceptable HTTP status codes")
	flag.StringVar(&cfg.HostOverrides, "host-overrides", "", "comma-separated k6 DNS host overrides as host=ip entries")
	flag.StringVar(&cfg.VUs, "vus", "1", "k6 virtual users")
	flag.StringVar(&cfg.Duration, "duration", "30s", "k6 duration")
	flag.StringVar(&cfg.HTTPFailedThreshold, "http-failed-threshold", "rate<0.01", "k6 threshold for http_req_failed")
	flag.StringVar(&cfg.SleepSeconds, "sleep-seconds", "1", "sleep seconds between requests in each VU")
	flag.StringVar(&portForwardReadyWait, "port-forward-ready-timeout", "10s", "timeout waiting for kubectl port-forward readiness")
	flag.Parse()

	var err error
	cfg.PortForwardReadyWait, err = time.ParseDuration(portForwardReadyWait)
	exitIfErr(err)
	exitIfErr(validateConfig(cfg))
	exitIfErr(runLoad(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg loadConfig) error {
	if cfg.K6 == "" {
		return errors.New("--k6 is required")
	}
	if cfg.Script == "" {
		return errors.New("--script is required")
	}
	if cfg.URL == "" && cfg.Surface == "" {
		return errors.New("pass --surface or --url")
	}
	if _, err := strconv.Atoi(cfg.VUs); err != nil {
		return fmt.Errorf("--vus must be an integer: %w", err)
	}
	if _, err := strconv.ParseFloat(cfg.SleepSeconds, 64); err != nil {
		return fmt.Errorf("--sleep-seconds must be numeric: %w", err)
	}
	if _, err := normalizeHostOverrides(cfg.HostOverrides); err != nil {
		return err
	}
	if cfg.PortForwardReadyWait <= 0 {
		return errors.New("--port-forward-ready-timeout must be positive")
	}
	if cfg.URL == "" {
		if _, err := resolveTarget(cfg, 0); err != nil {
			return err
		}
		if cfg.Kubectl == "" {
			return errors.New("--kubectl is required for built-in surface readiness preflight")
		}
		if cfg.WaitTimeout == "" {
			return errors.New("--wait-timeout must not be empty for built-in surface readiness preflight")
		}
	}
	return nil
}

func runLoad(ctx context.Context, cfg loadConfig) error {
	hostOverrides, err := normalizeHostOverrides(cfg.HostOverrides)
	if err != nil {
		return err
	}
	spec, cleanup, err := prepareTarget(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	if cfg.ExpectedStatuses != "" {
		spec.ExpectedStatuses = cfg.ExpectedStatuses
	}

	fmt.Printf("guardian cozystack http load\n")
	fmt.Printf("surface=%s stage=%s url=%s expectedStatuses=%s vus=%s duration=%s\n",
		surfaceLabel(cfg),
		cfg.Stage,
		spec.URL,
		spec.ExpectedStatuses,
		cfg.VUs,
		cfg.Duration,
	)
	if hostOverrides != "" {
		fmt.Printf("hostOverrides=%s\n", hostOverrides)
	}

	runner := k6Runner{
		bin:    cfg.K6,
		script: cfg.Script,
		env: map[string]string{
			"TARGET_URL":                   spec.URL,
			"EXPECTED_STATUSES":            spec.ExpectedStatuses,
			"REQUEST_NAME":                 spec.RequestName,
			"GUARDIAN_SURFACE":             surfaceLabel(cfg),
			"GUARDIAN_STAGE":               cfg.Stage,
			"K6_VUS":                       cfg.VUs,
			"K6_DURATION":                  cfg.Duration,
			"K6_HTTP_REQ_FAILED_THRESHOLD": cfg.HTTPFailedThreshold,
			"K6_SLEEP_SECONDS":             cfg.SleepSeconds,
		},
	}
	if hostOverrides != "" {
		runner.env["K6_HOSTS"] = hostOverrides
	}
	return runner.run(ctx)
}

var hostOverrideHostRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func normalizeHostOverrides(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}

	entries := map[string]string{}
	for _, rawEntry := range strings.Split(value, ",") {
		entry := strings.TrimSpace(rawEntry)
		if entry == "" {
			return "", errors.New("--host-overrides contains an empty host=ip entry")
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("--host-overrides entry %q must be host=ip", entry)
		}
		host := strings.ToLower(strings.TrimSpace(parts[0]))
		ip := strings.TrimSpace(parts[1])
		if !hostOverrideHostRE.MatchString(host) {
			return "", fmt.Errorf("--host-overrides host %q must be a DNS hostname without a port", host)
		}
		if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("--host-overrides IP for %q is invalid: %q", host, ip)
		}
		if _, exists := entries[host]; exists {
			return "", fmt.Errorf("--host-overrides contains duplicate host %q", host)
		}
		entries[host] = ip
	}

	hosts := make([]string, 0, len(entries))
	for host := range entries {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)

	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, host+"="+entries[host])
	}
	return strings.Join(out, ","), nil
}

func prepareTarget(ctx context.Context, cfg loadConfig) (targetSpec, func(), error) {
	if cfg.URL != "" {
		return targetSpec{
			URL:              cfg.URL,
			ExpectedStatuses: defaultExpectedStatuses(cfg.ExpectedStatuses),
			RequestName:      requestName(surfaceLabel(cfg), cfg.Stage),
		}, func() {}, nil
	}

	spec, err := resolveTarget(cfg, 0)
	if err != nil {
		return targetSpec{}, nil, err
	}
	if err := waitSurfaceReady(ctx, cfg); err != nil {
		return targetSpec{}, nil, err
	}
	if !spec.NeedsOpenBaoPort {
		return spec, func() {}, nil
	}
	port, err := freeLocalPort()
	if err != nil {
		return targetSpec{}, nil, err
	}
	spec, err = resolveTarget(cfg, port)
	if err != nil {
		return targetSpec{}, nil, err
	}
	forward, err := startPortForward(ctx, cfg, port)
	if err != nil {
		return targetSpec{}, nil, err
	}
	return spec, forward.Stop, nil
}

func waitSurfaceReady(ctx context.Context, cfg loadConfig) error {
	checks, err := surfaceReadinessChecks(cfg)
	if err != nil {
		return err
	}
	for _, check := range checks {
		if err := runKubectl(ctx, cfg, check.Label, check.Args...); err != nil {
			return err
		}
	}
	return nil
}

func surfaceReadinessChecks(cfg loadConfig) ([]kubectlCommand, error) {
	stage := strings.ToLower(cfg.Stage)
	surface := normalizeSurface(cfg.Surface)
	switch surface {
	case "harbor":
		namespace, err := namespaceForStage(stage)
		if err != nil {
			return nil, err
		}
		ref := "harbors.apps.cozystack.io/guardian"
		return []kubectlCommand{
			{
				Label: "Harbor app yaml",
				Args:  []string{"-n", namespace, "get", ref, "-o", "yaml"},
			},
			{
				Label: "wait Harbor app Ready",
				Args:  []string{"-n", namespace, "wait", "--for=condition=Ready", ref, "--timeout=" + cfg.WaitTimeout},
			},
			{
				Label: "wait Harbor workloads Ready",
				Args:  []string{"-n", namespace, "wait", "--for=condition=WorkloadsReady", ref, "--timeout=" + cfg.WaitTimeout},
			},
		}, nil
	case "dashboard":
		if stage != "root" {
			return nil, errors.New("dashboard is a root management-cluster surface; use --stage root")
		}
		return []kubectlCommand{
			{
				Label: "dashboard console deployment yaml",
				Args:  []string{"-n", "cozy-dashboard", "get", "deployment/cozy-dashboard-console", "-o", "yaml"},
			},
			{
				Label: "dashboard gatekeeper deployment yaml",
				Args:  []string{"-n", "cozy-dashboard", "get", "deployment/incloud-web-gatekeeper", "-o", "yaml"},
			},
			{
				Label: "wait dashboard console deployment Available",
				Args:  []string{"-n", "cozy-dashboard", "wait", "--for=condition=Available", "deployment/cozy-dashboard-console", "--timeout=" + cfg.WaitTimeout},
			},
			{
				Label: "wait dashboard gatekeeper deployment Available",
				Args:  []string{"-n", "cozy-dashboard", "wait", "--for=condition=Available", "deployment/incloud-web-gatekeeper", "--timeout=" + cfg.WaitTimeout},
			},
		}, nil
	case "openbao":
		if stage != "root" {
			return nil, errors.New("OpenBao is a root management-cluster surface; use --stage root")
		}
		return []kubectlCommand{
			{
				Label: "OpenBao app yaml",
				Args:  []string{"-n", "tenant-root", "get", "openbaos.apps.cozystack.io/guardian", "-o", "yaml"},
			},
			{
				Label: "OpenBao statefulset yaml",
				Args:  []string{"-n", "tenant-root", "get", "statefulset.apps/openbao-guardian", "-o", "yaml"},
			},
			{
				Label: "wait OpenBao app Ready",
				Args:  []string{"-n", "tenant-root", "wait", "--for=condition=Ready", "openbaos.apps.cozystack.io/guardian", "--timeout=" + cfg.WaitTimeout},
			},
			{
				Label: "wait OpenBao statefulset ready replicas",
				Args:  []string{"-n", "tenant-root", "wait", "--for=jsonpath={.status.readyReplicas}=3", "statefulset.apps/openbao-guardian", "--timeout=" + cfg.WaitTimeout},
			},
		}, nil
	default:
		return nil, fmt.Errorf("--surface %q is not one of harbor, dashboard, openbao, custom", cfg.Surface)
	}
}

func resolveTarget(cfg loadConfig, localPort int) (targetSpec, error) {
	stage := strings.ToLower(cfg.Stage)
	surface := normalizeSurface(cfg.Surface)
	if surface == "custom" && cfg.URL == "" {
		return targetSpec{}, errors.New("surface custom requires --url")
	}
	if surface == "" {
		return targetSpec{}, errors.New("--surface is required when --url is not set")
	}
	switch surface {
	case "harbor":
		host, err := harborHost(stage)
		if err != nil {
			return targetSpec{}, err
		}
		return targetSpec{
			URL:              "https://" + host + "/v2/",
			ExpectedStatuses: defaultExpectedStatusesOr(cfg.ExpectedStatuses, "200,401"),
			RequestName:      requestName(surface, stage),
		}, nil
	case "dashboard":
		if stage != "root" {
			return targetSpec{}, errors.New("dashboard is a root management-cluster surface; use --stage root")
		}
		return targetSpec{
			URL:              "https://dashboard.guardianintelligence.org/",
			ExpectedStatuses: defaultExpectedStatusesOr(cfg.ExpectedStatuses, "200,302"),
			RequestName:      requestName(surface, stage),
		}, nil
	case "openbao":
		if stage != "root" {
			return targetSpec{}, errors.New("OpenBao is a root management-cluster surface; use --stage root")
		}
		if localPort == 0 {
			return targetSpec{
				ExpectedStatuses: defaultExpectedStatusesOr(cfg.ExpectedStatuses, "200,429,472,473,501,503"),
				RequestName:      requestName(surface, stage),
				NeedsOpenBaoPort: true,
			}, nil
		}
		return targetSpec{
			URL:              fmt.Sprintf("http://127.0.0.1:%d/v1/sys/health", localPort),
			ExpectedStatuses: defaultExpectedStatusesOr(cfg.ExpectedStatuses, "200,429,472,473,501,503"),
			RequestName:      requestName(surface, stage),
			NeedsOpenBaoPort: true,
		}, nil
	default:
		return targetSpec{}, fmt.Errorf("--surface %q is not one of harbor, dashboard, openbao, custom", cfg.Surface)
	}
}

func normalizeSurface(surface string) string {
	switch strings.ToLower(surface) {
	case "harbor", "registry":
		return "harbor"
	case "dashboard", "cozystack-dashboard":
		return "dashboard"
	case "openbao", "bao", "vault":
		return "openbao"
	case "custom":
		return "custom"
	default:
		return strings.ToLower(surface)
	}
}

func namespaceForStage(stage string) (string, error) {
	switch stage {
	case "root":
		return "tenant-root", nil
	default:
		return "", fmt.Errorf("stage %q is not root", stage)
	}
}

func harborHost(stage string) (string, error) {
	switch stage {
	case "root":
		return "harbor.guardianintelligence.org", nil
	default:
		return "", errors.New("harbor supports --stage root")
	}
}

func defaultExpectedStatuses(value string) string {
	return defaultExpectedStatusesOr(value, "200")
}

func defaultExpectedStatusesOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func surfaceLabel(cfg loadConfig) string {
	if cfg.Surface == "" {
		return "custom"
	}
	return normalizeSurface(cfg.Surface)
}

func requestName(surface, stage string) string {
	return "guardian-" + surface + "-" + stage
}

func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

type portForward struct {
	cmd *exec.Cmd
}

func startPortForward(ctx context.Context, cfg loadConfig, localPort int) (*portForward, error) {
	args := openBaoPortForwardArgs(cfg, localPort)
	cmd := exec.CommandContext(ctx, cfg.Kubectl, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ready := make(chan string, 1)
	done := make(chan string, 2)
	readPortForwardOutput := func(scanner *bufio.Scanner) {
		var buf bytes.Buffer
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')
			if strings.Contains(line, "Forwarding from") {
				select {
				case ready <- line:
				default:
				}
			}
		}
		done <- buf.String()
	}
	go readPortForwardOutput(bufio.NewScanner(stdout))
	go readPortForwardOutput(bufio.NewScanner(stderr))

	timer := time.NewTimer(cfg.PortForwardReadyWait)
	defer timer.Stop()
	select {
	case <-ready:
		fmt.Printf("kubectl port-forward openbao established on 127.0.0.1:%d\n", localPort)
		return &portForward{cmd: cmd}, nil
	case <-timer.C:
		_ = cmd.Process.Kill()
		output := drainOutput(done)
		return nil, fmt.Errorf("timed out waiting for OpenBao port-forward readiness: %s", output)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}
}

func openBaoPortForwardArgs(cfg loadConfig, localPort int) []string {
	return kubectlArgs(cfg, "-n", "tenant-root", "port-forward", "--address", "127.0.0.1", "svc/openbao-guardian", fmt.Sprintf("%d:8200", localPort))
}

func drainOutput(done chan string) string {
	var out strings.Builder
	for i := 0; i < cap(done); i++ {
		select {
		case value := <-done:
			out.WriteString(value)
		default:
		}
	}
	return out.String()
}

func (p *portForward) Stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
}

func runKubectl(ctx context.Context, cfg loadConfig, label string, args ...string) error {
	cmd := exec.CommandContext(ctx, cfg.Kubectl, kubectlArgs(cfg, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	fmt.Printf("\n## %s\n", label)
	err := cmd.Run()
	fmt.Print(out.String())
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func kubectlArgs(cfg loadConfig, args ...string) []string {
	out := kubectlBaseArgs(cfg)
	out = append(out, args...)
	return out
}

func kubectlBaseArgs(cfg loadConfig) []string {
	out := []string{}
	if cfg.Kubeconfig != "" {
		out = append(out, "--kubeconfig", cfg.Kubeconfig)
	}
	if cfg.RequestTimeout != "" {
		out = append(out, "--request-timeout="+cfg.RequestTimeout)
	}
	return out
}

type k6Runner struct {
	bin    string
	script string
	env    map[string]string
}

func (r k6Runner) run(ctx context.Context) error {
	args := []string{
		"run",
		"--summary-trend-stats",
		"avg,min,med,p(95),p(99),max",
		r.script,
	}
	fmt.Printf("\n## k6 run\n")
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = os.Environ()
	for key, value := range r.env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	fmt.Print(buf.String())
	if err != nil {
		return fmt.Errorf("k6 run: %w", err)
	}
	return nil
}
