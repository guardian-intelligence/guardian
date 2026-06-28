package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type bootstrapConfig struct {
	Kubectl              string
	Tofu                 string
	Kubeconfig           string
	KubeAPIServer        string
	RequestTimeout       string
	WaitTimeout          string
	PortForwardReadyWait time.Duration
	Namespace            string
	StatefulSet          string
	Service              string
	Root                 string
	BackendEndpoint      string
	Mode                 string
	AuthPath             string
	OpsServiceAccount    string
	OpsRole              string
	TokenAudience        string
	TokenDuration        string
	LoginVerifyTimeout   time.Duration
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	kubeAPIServer  string
	requestTimeout string
	namespace      string
}

type portForward struct {
	cmd *exec.Cmd
}

var dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func main() {
	var cfg bootstrapConfig
	var portForwardReadyWait string
	var loginVerifyTimeout string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Tofu, "tofu", "", "path to the repo-pinned OpenTofu runner")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API server override for off-VLAN proof runs")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "5m", "timeout waiting for OpenBao StatefulSet readiness")
	flag.StringVar(&portForwardReadyWait, "port-forward-ready-timeout", "10s", "timeout waiting for kubectl port-forward readiness")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-guardian", "OpenBao namespace")
	flag.StringVar(&cfg.StatefulSet, "statefulset", "guardian-openbao", "OpenBao StatefulSet name")
	flag.StringVar(&cfg.Service, "service", "guardian-openbao-active", "OpenBao active Service name")
	flag.StringVar(&cfg.Root, "root", "", "OpenTofu root to bootstrap the OpenBao ops-controller identity")
	flag.StringVar(&cfg.BackendEndpoint, "backend-endpoint", "", "S3-compatible OpenTofu backend endpoint")
	flag.StringVar(&cfg.Mode, "mode", "apply", "OpenTofu operation: plan or apply")
	flag.StringVar(&cfg.AuthPath, "auth-path", "kubernetes", "OpenBao Kubernetes auth mount path")
	flag.StringVar(&cfg.OpsServiceAccount, "ops-service-account", "openbao-ops-controller", "ops-controller Kubernetes ServiceAccount name")
	flag.StringVar(&cfg.OpsRole, "ops-role", "guardian-openbao-ops-controller", "ops-controller OpenBao Kubernetes auth role")
	flag.StringVar(&cfg.TokenAudience, "token-audience", "openbao", "TokenRequest audience for OpenBao Kubernetes auth")
	flag.StringVar(&cfg.TokenDuration, "token-duration", "10m", "TokenRequest duration for bootstrap login verification")
	flag.StringVar(&loginVerifyTimeout, "login-verify-timeout", "15s", "timeout for post-apply OpenBao Kubernetes login verification")
	flag.Parse()

	var err error
	cfg.PortForwardReadyWait, err = time.ParseDuration(portForwardReadyWait)
	exitIfErr(err)
	cfg.LoginVerifyTimeout, err = time.ParseDuration(loginVerifyTimeout)
	exitIfErr(err)
	exitIfErr(validateConfig(cfg))
	exitIfErr(runBootstrap(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg bootstrapConfig) error {
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
	if cfg.LoginVerifyTimeout <= 0 {
		return errors.New("--login-verify-timeout must be positive")
	}
	if _, err := time.ParseDuration(cfg.TokenDuration); err != nil {
		return fmt.Errorf("--token-duration: %w", err)
	}
	for label, value := range map[string]string{
		"namespace":           cfg.Namespace,
		"statefulset":         cfg.StatefulSet,
		"service":             cfg.Service,
		"auth-path":           cfg.AuthPath,
		"ops-service-account": cfg.OpsServiceAccount,
	} {
		if !dnsSubdomainRE.MatchString(value) {
			return fmt.Errorf("--%s %q is not a Kubernetes DNS subdomain", label, value)
		}
	}
	for label, value := range map[string]string{
		"ops-role":       cfg.OpsRole,
		"token-audience": cfg.TokenAudience,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, " \t\r\n") {
			return fmt.Errorf("--%s must be non-empty and must not contain whitespace", label)
		}
	}
	switch cfg.Mode {
	case "plan", "apply":
	default:
		return fmt.Errorf("--mode %q is not one of plan, apply", cfg.Mode)
	}
	return nil
}

func runBootstrap(ctx context.Context, cfg bootstrapConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		kubeAPIServer:  cfg.KubeAPIServer,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}

	fmt.Printf("guardian cozystack openbao bootstrap\n")
	fmt.Printf("namespace=%s statefulset=%s service=%s root=%s mode=%s\n", cfg.Namespace, cfg.StatefulSet, cfg.Service, cfg.Root, cfg.Mode)

	if err := waitOpenBaoBootstrapReady(ctx, runner, cfg.StatefulSet, cfg.Service, cfg.WaitTimeout); err != nil {
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
	if err := runTofu(ctx, cfg.Tofu, "tofu "+cfg.Mode+" openbao-root-bootstrap", tofuRunArgs(cfg.Mode, cfg.Root, openbaoAddr), env, token); err != nil {
		return err
	}
	if cfg.Mode == "apply" {
		return verifyOpsControllerLogin(ctx, runner, openbaoAddr, cfg)
	}
	return nil
}

func waitOpenBaoBootstrapReady(ctx context.Context, runner kubectlRunner, statefulSet, service, timeout string) error {
	waitTimeout, err := time.ParseDuration(timeout)
	if err != nil {
		return fmt.Errorf("parse --wait-timeout %q: %w", timeout, err)
	}
	if waitTimeout <= 0 {
		return errors.New("--wait-timeout must be positive")
	}

	deadline := time.Now().Add(waitTimeout)
	if err := waitOpenBaoStatefulSetQuorum(ctx, runner, statefulSet, deadline); err != nil {
		return err
	}
	return waitOpenBaoActiveEndpoints(ctx, runner, service, deadline)
}

func waitOpenBaoStatefulSetQuorum(ctx context.Context, runner kubectlRunner, statefulSet string, deadline time.Time) error {
	var last string
	for {
		out, err := runner.output(ctx, "OpenBao StatefulSet status", "get", "statefulset.apps/"+statefulSet, "-o", "json")
		if err != nil {
			last = err.Error()
		} else {
			status, err := parseStatefulSetReplicaStatus(out)
			if err != nil {
				last = err.Error()
			} else {
				required := requiredReadyReplicas(status.Replicas)
				if status.ReadyReplicas >= required {
					fmt.Printf("OpenBao StatefulSet quorum ready: readyReplicas=%d required=%d replicas=%d\n", status.ReadyReplicas, required, status.Replicas)
					return nil
				}
				last = fmt.Sprintf("readyReplicas=%d required=%d replicas=%d", status.ReadyReplicas, required, status.Replicas)
			}
		}
		if err := sleepUntilNextPoll(ctx, deadline, "timed out waiting for OpenBao StatefulSet quorum readiness", last); err != nil {
			return err
		}
	}
}

func waitOpenBaoActiveEndpoints(ctx context.Context, runner kubectlRunner, service string, deadline time.Time) error {
	var last string
	for {
		out, err := runner.output(ctx, "OpenBao active EndpointSlices", "get", "endpointslices.discovery.k8s.io", "-l", "kubernetes.io/service-name="+service, "-o", "json")
		if err != nil {
			last = err.Error()
		} else {
			addresses, err := parseReadyEndpointSliceAddresses(out)
			if err != nil {
				last = err.Error()
			} else if addresses > 0 {
				fmt.Printf("OpenBao active service endpoints ready: service=%s readyAddresses=%d\n", service, addresses)
				return nil
			} else {
				last = fmt.Sprintf("service=%s readyAddresses=0", service)
			}
		}
		if err := sleepUntilNextPoll(ctx, deadline, "timed out waiting for OpenBao active service endpoints", last); err != nil {
			return err
		}
	}
}

func sleepUntilNextPoll(ctx context.Context, deadline time.Time, message, last string) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		if last == "" {
			return errors.New(message)
		}
		return fmt.Errorf("%s: %s", message, last)
	}
	sleep := 5 * time.Second
	if remaining < sleep {
		sleep = remaining
	}
	timer := time.NewTimer(sleep)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type replicaStatus struct {
	Replicas      int
	ReadyReplicas int
}

func parseStatefulSetReplicaStatus(raw string) (replicaStatus, error) {
	var doc struct {
		Spec struct {
			Replicas *int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return replicaStatus{}, err
	}
	replicas := 1
	if doc.Spec.Replicas != nil {
		replicas = *doc.Spec.Replicas
	}
	if replicas <= 0 {
		return replicaStatus{}, fmt.Errorf("StatefulSet replicas must be positive, got %d", replicas)
	}
	if doc.Status.ReadyReplicas < 0 {
		return replicaStatus{}, fmt.Errorf("StatefulSet readyReplicas must be non-negative, got %d", doc.Status.ReadyReplicas)
	}
	return replicaStatus{Replicas: replicas, ReadyReplicas: doc.Status.ReadyReplicas}, nil
}

func requiredReadyReplicas(replicas int) int {
	return replicas/2 + 1
}

func parseReadyEndpointSliceAddresses(raw string) (int, error) {
	var list struct {
		Items []struct {
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready *bool `json:"ready"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return 0, err
	}
	total := 0
	for _, item := range list.Items {
		for _, endpoint := range item.Endpoints {
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}
			total += len(endpoint.Addresses)
		}
	}
	return total, nil
}

func verifyOpsControllerLogin(ctx context.Context, runner kubectlRunner, openbaoAddr string, cfg bootstrapConfig) error {
	jwt, err := serviceAccountToken(ctx, runner, cfg.OpsServiceAccount, cfg.TokenAudience, cfg.TokenDuration)
	if err != nil {
		return err
	}
	if err := openBaoKubernetesLogin(ctx, openbaoAddr, cfg.AuthPath, cfg.OpsRole, jwt, cfg.LoginVerifyTimeout); err != nil {
		return err
	}
	fmt.Printf("verified OpenBao Kubernetes auth login for serviceAccount=%s role=%s without printing token material\n", cfg.OpsServiceAccount, cfg.OpsRole)
	return nil
}

func serviceAccountToken(ctx context.Context, runner kubectlRunner, serviceAccount, audience, duration string) (string, error) {
	out, err := runner.output(ctx, "ops-controller ServiceAccount token request", serviceAccountTokenArgs(serviceAccount, audience, duration)...)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(out)
	if token == "" {
		return "", errors.New("ops-controller ServiceAccount token request returned an empty token")
	}
	return token, nil
}

func serviceAccountTokenArgs(serviceAccount, audience, duration string) []string {
	return []string{
		"create",
		"token",
		serviceAccount,
		"--audience=" + audience,
		"--duration=" + duration,
	}
}

func openBaoKubernetesLogin(ctx context.Context, openbaoAddr, authPath, role, jwt string, timeout time.Duration) error {
	loginURL, err := openBaoKubernetesLoginURL(openbaoAddr, authPath)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{
		"role": role,
		"jwt":  jwt,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("verify OpenBao Kubernetes auth login: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("read OpenBao Kubernetes auth login response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("verify OpenBao Kubernetes auth login: status=%s body=%s", resp.Status, strings.TrimSpace(redactToken(string(raw), jwt)))
	}
	var decoded struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("decode OpenBao Kubernetes auth login response: %w", err)
	}
	if decoded.Auth.ClientToken == "" {
		return errors.New("OpenBao Kubernetes auth login response did not include a client token")
	}
	return nil
}

func openBaoKubernetesLoginURL(openbaoAddr, authPath string) (string, error) {
	base, err := url.Parse(strings.TrimRight(openbaoAddr, "/"))
	if err != nil {
		return "", err
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("OpenBao address %q must include scheme and host", openbaoAddr)
	}
	authPath = strings.Trim(authPath, "/")
	if authPath == "" {
		return "", errors.New("OpenBao auth path must not be empty")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/auth/" + url.PathEscape(authPath) + "/login"
	return base.String(), nil
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
	if r.kubeAPIServer != "" {
		out = append(out, "--server", r.kubeAPIServer)
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
