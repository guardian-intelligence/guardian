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
	"strings"
	"time"
)

type applyConfig struct {
	Kubectl              string
	Tofu                 string
	Kubeconfig           string
	RequestTimeout       string
	WaitTimeout          string
	PortForwardReadyWait time.Duration
	Namespace            string
	StatefulSet          string
	Service              string
	Root                 string
	BackendEndpoint      string
	Mode                 string
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	requestTimeout string
	namespace      string
}

type portForward struct {
	cmd *exec.Cmd
}

var dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func main() {
	var cfg applyConfig
	var portForwardReadyWait string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Tofu, "tofu", "", "path to the repo-pinned OpenTofu runner")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "5m", "timeout waiting for OpenBao StatefulSet readiness")
	flag.StringVar(&portForwardReadyWait, "port-forward-ready-timeout", "10s", "timeout waiting for kubectl port-forward readiness")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-guardian", "OpenBao namespace")
	flag.StringVar(&cfg.StatefulSet, "statefulset", "guardian-openbao", "OpenBao StatefulSet name")
	flag.StringVar(&cfg.Service, "service", "guardian-openbao", "OpenBao Service name")
	flag.StringVar(&cfg.Root, "root", "", "OpenTofu root to apply")
	flag.StringVar(&cfg.BackendEndpoint, "backend-endpoint", "", "S3-compatible OpenTofu backend endpoint")
	flag.StringVar(&cfg.Mode, "mode", "apply", "OpenTofu operation: plan or apply")
	flag.Parse()

	var err error
	cfg.PortForwardReadyWait, err = time.ParseDuration(portForwardReadyWait)
	exitIfErr(err)
	exitIfErr(validateConfig(cfg))
	exitIfErr(runApply(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg applyConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if cfg.Tofu == "" {
		return errors.New("--tofu is required")
	}
	if cfg.Root == "" {
		return errors.New("--root is required")
	}
	if cfg.BackendEndpoint == "" {
		return errors.New("--backend-endpoint is required")
	}
	if cfg.PortForwardReadyWait <= 0 {
		return errors.New("--port-forward-ready-timeout must be positive")
	}
	for label, value := range map[string]string{
		"namespace":   cfg.Namespace,
		"statefulset": cfg.StatefulSet,
		"service":     cfg.Service,
	} {
		if !dnsSubdomainRE.MatchString(value) {
			return fmt.Errorf("--%s %q is not a Kubernetes DNS subdomain", label, value)
		}
	}
	switch cfg.Mode {
	case "plan", "apply":
	default:
		return fmt.Errorf("--mode %q is not one of plan, apply", cfg.Mode)
	}
	return nil
}

func runApply(ctx context.Context, cfg applyConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}

	fmt.Printf("guardian cozystack openbao apply\n")
	fmt.Printf("namespace=%s statefulset=%s service=%s root=%s mode=%s\n", cfg.Namespace, cfg.StatefulSet, cfg.Service, cfg.Root, cfg.Mode)

	if err := waitStatefulSetReady(ctx, runner, cfg.StatefulSet, cfg.WaitTimeout); err != nil {
		return err
	}
	token, tokenSource, err := rootTokenFromEnv()
	if err != nil {
		return err
	}
	fmt.Printf("read OpenBao root token from %s without printing secret material\n", tokenSource)

	localPort, err := freeLocalPort()
	if err != nil {
		return err
	}
	pf, err := startPortForward(ctx, runner, cfg.Service, localPort, cfg.PortForwardReadyWait)
	if err != nil {
		return err
	}
	defer pf.stop()

	openbaoAddr := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	env := tofuEnv(os.Environ(), openbaoAddr, token)
	if err := runTofu(ctx, cfg.Tofu, "tofu init openbao-root-bootstrap", tofuInitArgs(cfg.Root, cfg.BackendEndpoint), env, token); err != nil {
		return err
	}
	return runTofu(ctx, cfg.Tofu, "tofu "+cfg.Mode+" openbao-root-bootstrap", tofuRunArgs(cfg.Mode, cfg.Root, openbaoAddr), env, token)
}

func waitStatefulSetReady(ctx context.Context, runner kubectlRunner, statefulSet, timeout string) error {
	replicas, err := runner.output(ctx, "OpenBao StatefulSet replicas", "get", "statefulset.apps/"+statefulSet, "-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return err
	}
	want := strings.TrimSpace(replicas)
	if want == "" {
		return fmt.Errorf("OpenBao StatefulSet %s has empty spec.replicas", statefulSet)
	}
	return runner.run(ctx, "wait OpenBao StatefulSet ready", "wait", "--for=jsonpath={.status.readyReplicas}="+want, "statefulset.apps/"+statefulSet, "--timeout="+timeout)
}

func rootTokenFromEnv() (string, string, error) {
	for _, key := range []string{"BAO_TOKEN", "VAULT_TOKEN"} {
		token := strings.TrimSpace(os.Getenv(key))
		if token != "" {
			return token, key, nil
		}
	}
	return "", "", errors.New("BAO_TOKEN or VAULT_TOKEN is required; do not store the OpenBao root token in Kubernetes")
}

func tofuInitArgs(root, endpoint string) []string {
	return []string{
		"-chdir=" + root,
		"init",
		"-input=false",
		"-reconfigure",
		"-backend-config=endpoint=" + endpoint,
	}
}

func tofuRunArgs(mode, root, openbaoAddr string) []string {
	args := []string{
		"-chdir=" + root,
		mode,
		"-input=false",
		"-var=openbao_addr=" + openbaoAddr,
	}
	if mode == "apply" {
		args = append(args, "-auto-approve")
	}
	return args
}

func runTofu(ctx context.Context, tofu, label string, args, env []string, token string) error {
	fmt.Printf("\n## %s\n", label)
	cmd := exec.CommandContext(ctx, tofu, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	fmt.Print(redactToken(string(out), token))
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func tofuEnv(base []string, openbaoAddr, token string) []string {
	env := append([]string{}, base...)
	env = putEnv(env, "BAO_ADDR", openbaoAddr)
	env = putEnv(env, "BAO_TOKEN", token)
	env = putEnv(env, "VAULT_ADDR", openbaoAddr)
	env = putEnv(env, "VAULT_TOKEN", token)
	env = putEnv(env, "VAULT_CLIENT_TIMEOUT", "120s")
	if !hasEnv(env, "AWS_EC2_METADATA_DISABLED") {
		env = append(env, "AWS_EC2_METADATA_DISABLED=true")
	}
	return env
}

func putEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func hasEnv(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func redactToken(out, token string) string {
	if token == "" {
		return out
	}
	return strings.ReplaceAll(out, token, "<redacted>")
}

func (r kubectlRunner) args(args ...string) []string {
	out := []string{}
	if r.kubeconfig != "" {
		out = append(out, "--kubeconfig", r.kubeconfig)
	}
	if r.requestTimeout != "" {
		out = append(out, "--request-timeout="+r.requestTimeout)
	}
	if r.namespace != "" {
		out = append(out, "-n", r.namespace)
	}
	out = append(out, args...)
	return out
}

func (r kubectlRunner) run(ctx context.Context, label string, args ...string) error {
	fmt.Printf("\n## %s\n", label)
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w\n%s", label, err, out)
	}
	return string(out), nil
}

func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func startPortForward(ctx context.Context, runner kubectlRunner, service string, localPort int, readyWait time.Duration) (*portForward, error) {
	args := openBaoPortForwardArgs(runner, service, localPort)
	cmd := exec.CommandContext(ctx, runner.bin, args...)
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

	timer := time.NewTimer(readyWait)
	defer timer.Stop()
	select {
	case <-ready:
		fmt.Printf("kubectl port-forward openbao established on 127.0.0.1:%d\n", localPort)
		return &portForward{cmd: cmd}, nil
	case <-timer.C:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("timed out waiting for OpenBao port-forward readiness: %s", drainOutput(done))
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ctx.Err()
	}
}

func openBaoPortForwardArgs(runner kubectlRunner, service string, localPort int) []string {
	return runner.args("port-forward", "--address", "127.0.0.1", "svc/"+service, fmt.Sprintf("%d:8200", localPort))
}

func (pf *portForward) stop() {
	if pf == nil || pf.cmd == nil || pf.cmd.Process == nil {
		return
	}
	_ = pf.cmd.Process.Kill()
	_ = pf.cmd.Wait()
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
