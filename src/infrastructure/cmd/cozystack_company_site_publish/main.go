package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const harborAdminUser = "admin"

type publishConfig struct {
	Kubectl                 string
	Kubeconfig              string
	RequestTimeout          string
	WaitTimeout             string
	Bazel                   string
	Target                  string
	Namespace               string
	Secret                  string
	Host                    string
	Project                 string
	ProjectPublic           bool
	PortForwardService      string
	PortForwardReadyTimeout string
	Workspace               string
}

type dockerConfig struct {
	Auths map[string]dockerAuth `json:"auths"`
}

type dockerAuth struct {
	Auth string `json:"auth"`
}

type harborProjectRequest struct {
	ProjectName string            `json:"project_name"`
	Public      bool              `json:"public"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type kubectlCommand struct {
	Label string
	Args  []string
}

var (
	dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	bazelTargetRE  = regexp.MustCompile(`^//[A-Za-z0-9_./+-]+:[A-Za-z0-9_.+-]+$`)
)

func main() {
	var cfg publishConfig
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "15m", "timeout waiting for Harbor readiness")
	flag.StringVar(&cfg.Bazel, "bazel", "bazelisk", "path to bazelisk")
	flag.StringVar(&cfg.Target, "target", "//src/products/company/site:push-harbor", "Bazel oci_push target to run")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-root", "namespace containing the root Harbor credentials Secret")
	flag.StringVar(&cfg.Secret, "secret", "harbor-guardian-credentials", "Harbor credentials Secret name")
	flag.StringVar(&cfg.Host, "host", "harbor.guardianintelligence.org", "Harbor registry host")
	flag.StringVar(&cfg.Project, "project", "guardian", "Harbor project that owns the company-site repository")
	flag.BoolVar(&cfg.ProjectPublic, "project-public", true, "Set the Harbor project public so cluster pulls do not require imagePullSecrets")
	flag.StringVar(&cfg.PortForwardService, "port-forward-service", "harbor-guardian", "Harbor frontend Service used for local publish port-forwarding")
	flag.StringVar(&cfg.PortForwardReadyTimeout, "port-forward-ready-timeout", "10s", "timeout waiting for local Harbor port-forward readiness")
	flag.StringVar(&cfg.Workspace, "workspace", ".", "workspace directory for bazelisk")
	flag.Parse()

	exitIfErr(validateConfig(cfg))
	exitIfErr(runPublish(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg publishConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if cfg.Bazel == "" {
		return errors.New("--bazel must not be empty")
	}
	if cfg.WaitTimeout == "" {
		return errors.New("--wait-timeout must not be empty")
	}
	for label, value := range map[string]string{
		"host":                 cfg.Host,
		"namespace":            cfg.Namespace,
		"project":              cfg.Project,
		"secret":               cfg.Secret,
		"port-forward-service": cfg.PortForwardService,
	} {
		if !dnsSubdomainRE.MatchString(value) {
			return fmt.Errorf("--%s %q is not a Kubernetes DNS subdomain", label, value)
		}
	}
	if !bazelTargetRE.MatchString(cfg.Target) {
		return fmt.Errorf("--target %q is not an absolute Bazel target", cfg.Target)
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		return errors.New("--workspace must not be empty")
	}
	if _, err := time.ParseDuration(cfg.PortForwardReadyTimeout); err != nil {
		return fmt.Errorf("--port-forward-ready-timeout %q is invalid: %w", cfg.PortForwardReadyTimeout, err)
	}
	return nil
}

func runPublish(ctx context.Context, cfg publishConfig) error {
	dir, err := os.MkdirTemp("", "guardian-company-site-publish-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	if err := waitHarborReady(ctx, cfg); err != nil {
		return err
	}

	password, err := harborAdminPassword(ctx, cfg)
	if err != nil {
		return err
	}

	forward, err := startPortForward(ctx, cfg)
	if err != nil {
		return err
	}
	defer forward.close()

	localRegistry := fmt.Sprintf("127.0.0.1:%d", forward.LocalPort)
	localCfg := cfg
	localCfg.Host = "http://" + localRegistry
	if err := ensureHarborProject(ctx, localCfg, password); err != nil {
		return err
	}
	config, err := dockerConfigPayload(localRegistry, harborAdminUser, password)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o600); err != nil {
		return fmt.Errorf("write Docker config: %w", err)
	}

	fmt.Printf("guardian company-site publish\n")
	fmt.Printf("target=%s host=%s localRegistry=%s project=%s namespace=%s secret=%s\n", cfg.Target, cfg.Host, localRegistry, cfg.Project, cfg.Namespace, cfg.Secret)
	fmt.Printf("using temporary Docker config; Harbor password is not printed or passed on argv\n")

	localRepository := localRegistry + "/" + cfg.Project + "/company-site"
	cmd := exec.CommandContext(ctx, cfg.Bazel, "run", cfg.Target, "--", "--repository", localRepository, "--insecure")
	cmd.Dir = cfg.Workspace
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dir)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	fmt.Print(redactSecret(out.String(), password))
	if err != nil {
		return fmt.Errorf("publish company-site image: %w", err)
	}
	fmt.Printf("company-site publish completed: target=%s host=%s\n", cfg.Target, cfg.Host)
	return nil
}

func ensureHarborProject(ctx context.Context, cfg publishConfig, password string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	endpoint, err := harborAPIURL(cfg.Host, "/api/v2.0/projects/"+url.PathEscape(cfg.Project))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(harborAdminUser, password)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("read Harbor project %q: %w", cfg.Project, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := updateHarborProjectVisibility(ctx, client, cfg, password); err != nil {
			return err
		}
		fmt.Printf("Harbor project %q already exists\n", cfg.Project)
		return nil
	case http.StatusNotFound:
		return createHarborProject(ctx, client, cfg, password)
	default:
		body := responseBody(resp, password)
		return fmt.Errorf("read Harbor project %q: %s%s", cfg.Project, resp.Status, body)
	}
}

func createHarborProject(ctx context.Context, client *http.Client, cfg publishConfig, password string) error {
	endpoint, err := harborAPIURL(cfg.Host, "/api/v2.0/projects")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(harborProjectRequest{
		ProjectName: cfg.Project,
		Public:      cfg.ProjectPublic,
		Metadata: map[string]string{
			"public": boolString(cfg.ProjectPublic),
		},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(harborAdminUser, password)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("create Harbor project %q: %w", cfg.Project, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		fmt.Printf("Harbor project %q created public=%s\n", cfg.Project, boolString(cfg.ProjectPublic))
		return nil
	case http.StatusConflict:
		if err := updateHarborProjectVisibility(ctx, client, cfg, password); err != nil {
			return err
		}
		fmt.Printf("Harbor project %q already exists\n", cfg.Project)
		return nil
	default:
		body := responseBody(resp, password)
		return fmt.Errorf("create Harbor project %q: %s%s", cfg.Project, resp.Status, body)
	}
}

func updateHarborProjectVisibility(ctx context.Context, client *http.Client, cfg publishConfig, password string) error {
	endpoint, err := harborAPIURL(cfg.Host, "/api/v2.0/projects/"+url.PathEscape(cfg.Project))
	if err != nil {
		return err
	}
	payload, err := json.Marshal(harborProjectRequest{
		Metadata: map[string]string{
			"public": boolString(cfg.ProjectPublic),
		},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(harborAdminUser, password)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("update Harbor project %q visibility: %w", cfg.Project, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := responseBody(resp, password)
		return fmt.Errorf("update Harbor project %q visibility: %s%s", cfg.Project, resp.Status, body)
	}
	fmt.Printf("Harbor project %q public=%s\n", cfg.Project, boolString(cfg.ProjectPublic))
	return nil
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func harborAPIURL(host, path string) (string, error) {
	base := host
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse Harbor URL %q: %w", host, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("parse Harbor URL %q: missing scheme or host", host)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func responseBody(resp *http.Response, password string) string {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || len(data) == 0 {
		return ""
	}
	body := strings.TrimSpace(redactSecret(string(data), password))
	if body == "" {
		return ""
	}
	return ": " + body
}

type portForward struct {
	Cmd       *exec.Cmd
	Cancel    context.CancelFunc
	Output    *bytes.Buffer
	LocalPort int
}

func startPortForward(ctx context.Context, cfg publishConfig) (*portForward, error) {
	readyWait, err := time.ParseDuration(cfg.PortForwardReadyTimeout)
	if err != nil {
		return nil, err
	}
	localPort, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	pfCtx, cancel := context.WithCancel(ctx)
	args := kubectlArgs(cfg,
		"-n", cfg.Namespace,
		"port-forward",
		"--address", "127.0.0.1",
		"svc/"+cfg.PortForwardService,
		fmt.Sprintf("%d:80", localPort),
	)
	cmd := exec.CommandContext(pfCtx, cfg.Kubectl, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start Harbor port-forward: %w", err)
	}
	forward := &portForward{
		Cmd:       cmd,
		Cancel:    cancel,
		Output:    &out,
		LocalPort: localPort,
	}
	if err := waitLocalPort(ctx, localPort, readyWait); err != nil {
		forward.close()
		return nil, fmt.Errorf("wait Harbor port-forward: %w\n%s", err, out.String())
	}
	fmt.Printf("kubectl port-forward Harbor established on 127.0.0.1:%d\n", localPort)
	return forward, nil
}

func (p *portForward) close() {
	if p == nil {
		return
	}
	p.Cancel()
	_ = p.Cmd.Wait()
}

func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected local listener address %T", listener.Addr())
	}
	return addr.Port, nil
}

func waitLocalPort(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitHarborReady(ctx context.Context, cfg publishConfig) error {
	for _, cmd := range harborReadinessChecks(cfg) {
		if err := runKubectl(ctx, cfg, cmd.Label, cmd.Args...); err != nil {
			return err
		}
	}
	return nil
}

func harborReadinessChecks(cfg publishConfig) []kubectlCommand {
	ref := "harbors.apps.cozystack.io/guardian"
	registry := "harbor-guardian-registry"
	return []kubectlCommand{
		{
			Label: "Harbor app yaml",
			Args:  []string{"-n", cfg.Namespace, "get", ref, "-o", "yaml"},
		},
		{
			Label: "Harbor registry bucket claim yaml",
			Args:  []string{"-n", cfg.Namespace, "get", "bucketclaims.objectstorage.k8s.io/" + registry, "-o", "yaml"},
		},
		{
			Label: "Harbor registry bucket access yaml",
			Args:  []string{"-n", cfg.Namespace, "get", "bucketaccesses.objectstorage.k8s.io/" + registry, "-o", "yaml"},
		},
		{
			Label: "wait Harbor app Ready",
			Args:  []string{"-n", cfg.Namespace, "wait", "--for=condition=Ready", ref, "--timeout=" + cfg.WaitTimeout},
		},
		{
			Label: "wait Harbor registry bucket ready",
			Args:  []string{"-n", cfg.Namespace, "wait", "--for=jsonpath={.status.bucketReady}=true", "bucketclaims.objectstorage.k8s.io/" + registry, "--timeout=" + cfg.WaitTimeout},
		},
		{
			Label: "wait Harbor registry bucket access granted",
			Args:  []string{"-n", cfg.Namespace, "wait", "--for=jsonpath={.status.accessGranted}=true", "bucketaccesses.objectstorage.k8s.io/" + registry, "--timeout=" + cfg.WaitTimeout},
		},
		{
			Label: "wait Harbor workloads Ready",
			Args:  []string{"-n", cfg.Namespace, "wait", "--for=condition=WorkloadsReady", ref, "--timeout=" + cfg.WaitTimeout},
		},
	}
}

func harborAdminPassword(ctx context.Context, cfg publishConfig) (string, error) {
	args := kubectlArgs(cfg, "-n", cfg.Namespace, "get", "secret/"+cfg.Secret, "-o", "jsonpath={.data.admin-password}")
	cmd := exec.CommandContext(ctx, cfg.Kubectl, args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("read Harbor admin password: %w\n%s", err, stderr.String())
		}
		return "", fmt.Errorf("read Harbor admin password: %w", err)
	}
	encoded := strings.TrimSpace(out.String())
	if encoded == "" {
		return "", errors.New("Harbor admin password secret key is empty")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode Harbor admin password: %w", err)
	}
	if len(decoded) == 0 {
		return "", errors.New("decoded Harbor admin password is empty")
	}
	return string(decoded), nil
}

func runKubectl(ctx context.Context, cfg publishConfig, label string, args ...string) error {
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

func kubectlArgs(cfg publishConfig, args ...string) []string {
	out := make([]string, 0, len(args)+4)
	if cfg.Kubeconfig != "" {
		out = append(out, "--kubeconfig", cfg.Kubeconfig)
	}
	if cfg.RequestTimeout != "" {
		out = append(out, "--request-timeout="+cfg.RequestTimeout)
	}
	out = append(out, args...)
	return out
}

func dockerConfigPayload(host, username, password string) ([]byte, error) {
	if host == "" || username == "" || password == "" {
		return nil, errors.New("host, username, and password are required")
	}
	payload := dockerConfig{
		Auths: map[string]dockerAuth{
			host: {
				Auth: base64.StdEncoding.EncodeToString([]byte(username + ":" + password)),
			},
		},
	}
	return json.MarshalIndent(payload, "", "  ")
}

func redactSecret(out, secret string) string {
	if secret == "" {
		return out
	}
	redacted := strings.ReplaceAll(out, secret, "<redacted>")
	redacted = strings.ReplaceAll(redacted, base64.StdEncoding.EncodeToString([]byte(harborAdminUser+":"+secret)), "<redacted-auth>")
	return redacted
}
