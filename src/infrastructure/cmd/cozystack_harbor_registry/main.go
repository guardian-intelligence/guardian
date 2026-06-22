package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	harborAdminUser = "admin"
	artifactType    = "application/vnd.guardian.harbor-registry-smoke.v1"
	payloadType     = "application/vnd.guardian.harbor-registry-smoke.payload.v1+text"
)

type harborRegistryConfig struct {
	Oras             string
	Kubectl          string
	Kubeconfig       string
	RequestTimeout   string
	Stage            string
	Namespace        string
	Host             string
	Repository       string
	Tag              string
	Iterations       int
	PayloadBytes     int
	AllowPlainHTTP   bool
	AllowInsecureTLS bool
	RegistryConfig   string
}

var (
	repositoryRE = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(\/[a-z0-9]+([._-][a-z0-9]+)*)*$`)
	tagRE        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

func main() {
	var cfg harborRegistryConfig
	flag.StringVar(&cfg.Oras, "oras", "", "path to oras")
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.Stage, "stage", "dev", "Guardian stage: root, dev, gamma, or prod")
	flag.StringVar(&cfg.Host, "host", "", "Harbor registry host; defaults from --stage")
	flag.StringVar(&cfg.Repository, "repository", "library/guardian-smoke", "Harbor repository path")
	flag.StringVar(&cfg.Tag, "tag", "", "tag or tag prefix; defaults to a UTC timestamp")
	flag.IntVar(&cfg.Iterations, "iterations", 1, "number of ORAS push/pull iterations")
	flag.IntVar(&cfg.PayloadBytes, "payload-bytes", 4096, "payload size per ORAS push")
	flag.BoolVar(&cfg.AllowPlainHTTP, "plain-http", false, "allow plain HTTP registry connections")
	flag.BoolVar(&cfg.AllowInsecureTLS, "insecure", false, "allow TLS registry connections without certificate verification")
	flag.StringVar(&cfg.RegistryConfig, "registry-config", "", "ORAS registry auth config path; defaults to a temporary file")
	flag.Parse()

	var err error
	cfg.Namespace, err = namespaceForStage(cfg.Stage)
	exitIfErr(err)
	if cfg.Host == "" {
		cfg.Host, err = harborHost(cfg.Stage)
		exitIfErr(err)
	}
	if cfg.Tag == "" {
		cfg.Tag = defaultTag(cfg.Stage, time.Now().UTC())
	}
	exitIfErr(validateConfig(cfg))
	exitIfErr(runSmoke(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func namespaceForStage(stage string) (string, error) {
	switch stage {
	case "root":
		return "tenant-root", nil
	case "dev", "gamma", "prod":
		return "tenant-" + stage, nil
	default:
		return "", fmt.Errorf("stage %q is not one of root, dev, gamma, prod", stage)
	}
}

func harborHost(stage string) (string, error) {
	switch stage {
	case "root":
		return "harbor.guardianintelligence.org", nil
	case "dev", "gamma", "prod":
		return "harbor." + stage + ".gi.org", nil
	default:
		return "", fmt.Errorf("stage %q is not one of root, dev, gamma, prod", stage)
	}
}

func defaultTag(stage string, now time.Time) string {
	return fmt.Sprintf("guardian-%s-%s", stage, now.UTC().Format("20060102t150405z"))
}

func validateConfig(cfg harborRegistryConfig) error {
	if cfg.Oras == "" {
		return errors.New("--oras is required")
	}
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if cfg.Host == "" {
		return errors.New("--host must not be empty")
	}
	if !repositoryRE.MatchString(cfg.Repository) {
		return fmt.Errorf("--repository %q is not an OCI repository path", cfg.Repository)
	}
	if !tagRE.MatchString(cfg.Tag) {
		return fmt.Errorf("--tag %q is not an OCI tag", cfg.Tag)
	}
	if cfg.Iterations <= 0 {
		return errors.New("--iterations must be positive")
	}
	if cfg.PayloadBytes <= 0 {
		return errors.New("--payload-bytes must be positive")
	}
	return nil
}

func runSmoke(ctx context.Context, cfg harborRegistryConfig) error {
	dir, err := os.MkdirTemp("", "guardian-harbor-registry-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	if cfg.RegistryConfig == "" {
		cfg.RegistryConfig = filepath.Join(dir, "oras-auth.json")
	}

	password, err := harborAdminPassword(ctx, cfg)
	if err != nil {
		return err
	}

	oras := orasRunner{
		bin:            cfg.Oras,
		registryConfig: cfg.RegistryConfig,
		plainHTTP:      cfg.AllowPlainHTTP,
		insecureTLS:    cfg.AllowInsecureTLS,
	}

	fmt.Printf("guardian cozystack harbor registry smoke\n")
	fmt.Printf("stage=%s namespace=%s host=%s repository=%s tag=%s iterations=%d payloadBytes=%d\n",
		cfg.Stage,
		cfg.Namespace,
		cfg.Host,
		cfg.Repository,
		cfg.Tag,
		cfg.Iterations,
		cfg.PayloadBytes,
	)

	if err := oras.runWithStdin(ctx, "oras login", password+"\n", "login", cfg.Host, "--username", harborAdminUser, "--password-stdin"); err != nil {
		return err
	}

	for i := 1; i <= cfg.Iterations; i++ {
		ref := registryRef(cfg, i)
		payload := payloadFor(cfg, i)
		payloadName := fmt.Sprintf("payload-%06d.txt", i)
		payloadPath := filepath.Join(dir, payloadName)
		pullDir := filepath.Join(dir, fmt.Sprintf("pull-%06d", i))
		if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
			return err
		}
		if err := os.MkdirAll(pullDir, 0o700); err != nil {
			return err
		}

		fmt.Printf("\n## iteration %d ref=%s payload_sha256=%x\n", i, ref, sha256.Sum256(payload))
		if err := oras.runInDir(ctx, dir, "oras push", "push", "--artifact-type", artifactType, ref, payloadName+":"+payloadType); err != nil {
			return err
		}
		if err := oras.run(ctx, "oras manifest fetch", "manifest", "fetch", ref); err != nil {
			return err
		}
		if err := oras.run(ctx, "oras pull", "pull", "--output", pullDir, ref); err != nil {
			return err
		}
		pulledPath := filepath.Join(pullDir, payloadName)
		pulled, err := os.ReadFile(pulledPath)
		if err != nil {
			return fmt.Errorf("read pulled payload %s: %w", pulledPath, err)
		}
		if !bytes.Equal(pulled, payload) {
			return fmt.Errorf("pulled payload mismatch for %s", ref)
		}
		fmt.Printf("pulled payload verified: ref=%s sha256=%x\n", ref, sha256.Sum256(pulled))
	}
	fmt.Printf("harbor registry smoke completed: host=%s repository=%s iterations=%d\n", cfg.Host, cfg.Repository, cfg.Iterations)
	return nil
}

func harborAdminPassword(ctx context.Context, cfg harborRegistryConfig) (string, error) {
	args := kubectlArgs(cfg, "-n", cfg.Namespace, "get", "secret/harbor-guardian-credentials", "-o", "jsonpath={.data.admin-password}")
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

func kubectlArgs(cfg harborRegistryConfig, args ...string) []string {
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

func registryRef(cfg harborRegistryConfig, iteration int) string {
	tag := cfg.Tag
	if cfg.Iterations > 1 {
		tag = fmt.Sprintf("%s-%06d", cfg.Tag, iteration)
	}
	return cfg.Host + "/" + cfg.Repository + ":" + tag
}

func payloadFor(cfg harborRegistryConfig, iteration int) []byte {
	header := fmt.Sprintf("guardian harbor registry smoke\nstage=%s\nhost=%s\nrepository=%s\niteration=%d\n", cfg.Stage, cfg.Host, cfg.Repository, iteration)
	var out bytes.Buffer
	for out.Len() < cfg.PayloadBytes {
		out.WriteString(header)
		out.WriteString(strconv.Itoa(out.Len()))
		out.WriteByte('\n')
	}
	return out.Bytes()[:cfg.PayloadBytes]
}

type orasRunner struct {
	bin            string
	registryConfig string
	plainHTTP      bool
	insecureTLS    bool
}

func (r orasRunner) baseArgs(args ...string) []string {
	out := make([]string, 0, len(args)+6)
	out = append(out, args...)
	out = append(out, "--registry-config", r.registryConfig)
	if r.plainHTTP {
		out = append(out, "--plain-http")
	}
	if r.insecureTLS {
		out = append(out, "--insecure")
	}
	return out
}

func (r orasRunner) run(ctx context.Context, label string, args ...string) error {
	return r.runWithStdin(ctx, label, "", args...)
}

func (r orasRunner) runInDir(ctx context.Context, dir string, label string, args ...string) error {
	return r.runWithStdinInDir(ctx, dir, label, "", args...)
}

func (r orasRunner) runWithStdin(ctx context.Context, label string, stdin string, args ...string) error {
	return r.runWithStdinInDir(ctx, "", label, stdin, args...)
}

func (r orasRunner) runWithStdinInDir(ctx context.Context, dir string, label string, stdin string, args ...string) error {
	fmt.Printf("\n## %s\n", label)
	cmd := exec.CommandContext(ctx, r.bin, r.baseArgs(args...)...)
	if dir != "" {
		cmd.Dir = dir
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	fmt.Print(buf.String())
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
