package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const harborAdminUser = "admin"

type publishConfig struct {
	Kubectl        string
	Kubeconfig     string
	RequestTimeout string
	Bazel          string
	Target         string
	Namespace      string
	Secret         string
	Host           string
	Workspace      string
}

type dockerConfig struct {
	Auths map[string]dockerAuth `json:"auths"`
}

type dockerAuth struct {
	Auth string `json:"auth"`
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
	flag.StringVar(&cfg.Bazel, "bazel", "bazelisk", "path to bazelisk")
	flag.StringVar(&cfg.Target, "target", "//src/products/company/site:push-harbor", "Bazel oci_push target to run")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-root", "namespace containing the root Harbor credentials Secret")
	flag.StringVar(&cfg.Secret, "secret", "harbor-guardian-credentials", "Harbor credentials Secret name")
	flag.StringVar(&cfg.Host, "host", "harbor.guardianintelligence.org", "Harbor registry host")
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
	for label, value := range map[string]string{
		"host":      cfg.Host,
		"namespace": cfg.Namespace,
		"secret":    cfg.Secret,
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
	return nil
}

func runPublish(ctx context.Context, cfg publishConfig) error {
	dir, err := os.MkdirTemp("", "guardian-company-site-publish-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	password, err := harborAdminPassword(ctx, cfg)
	if err != nil {
		return err
	}
	config, err := dockerConfigPayload(cfg.Host, harborAdminUser, password)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o600); err != nil {
		return fmt.Errorf("write Docker config: %w", err)
	}

	fmt.Printf("guardian company-site publish\n")
	fmt.Printf("target=%s host=%s namespace=%s secret=%s\n", cfg.Target, cfg.Host, cfg.Namespace, cfg.Secret)
	fmt.Printf("using temporary Docker config; Harbor password is not printed or passed on argv\n")

	cmd := exec.CommandContext(ctx, cfg.Bazel, "run", cfg.Target)
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
