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
	"strconv"
	"strings"
	"time"
)

const baoAddr = "http://127.0.0.1:8200"

type openBaoConfig struct {
	Kubectl         string
	Kubeconfig      string
	RequestTimeout  string
	WaitTimeout     string
	Namespace       string
	StatefulSet     string
	BootstrapSecret string
	Mode            string
	SnapshotName    string
}

type baoStatus struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
}

type initResult struct {
	UnsealKeysB64 []string `json:"unseal_keys_b64"`
	RootToken     string   `json:"root_token"`
}

type bootstrapMaterial struct {
	UnsealKey string
	RootToken string
}

var dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func main() {
	var cfg openBaoConfig
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "5m", "timeout for OpenBao StatefulSet readiness")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-root", "OpenBao namespace")
	flag.StringVar(&cfg.StatefulSet, "statefulset", "openbao-guardian", "OpenBao StatefulSet name")
	flag.StringVar(&cfg.BootstrapSecret, "bootstrap-secret", "openbao-guardian-bootstrap", "Kubernetes Secret for cluster-local OpenBao bootstrap material")
	flag.StringVar(&cfg.Mode, "mode", "status", "drill mode: status, init-unseal, or snapshot")
	flag.StringVar(&cfg.SnapshotName, "snapshot-name", "", "snapshot filename inside the OpenBao pod; defaults to a UTC timestamped name")
	flag.Parse()

	if cfg.SnapshotName == "" {
		cfg.SnapshotName = defaultSnapshotName(time.Now().UTC())
	}
	exitIfErr(validateConfig(cfg))
	exitIfErr(runDrill(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func defaultSnapshotName(now time.Time) string {
	return "guardian-openbao-" + now.Format("20060102t150405z") + ".snap"
}

func validateConfig(cfg openBaoConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	for label, value := range map[string]string{
		"namespace":        cfg.Namespace,
		"statefulset":      cfg.StatefulSet,
		"bootstrap-secret": cfg.BootstrapSecret,
	} {
		if !dnsSubdomainRE.MatchString(value) {
			return fmt.Errorf("--%s %q is not a Kubernetes DNS subdomain", label, value)
		}
	}
	switch cfg.Mode {
	case "status", "init-unseal", "snapshot":
	default:
		return fmt.Errorf("--mode %q is not one of status, init-unseal, snapshot", cfg.Mode)
	}
	if strings.Contains(cfg.SnapshotName, "/") || strings.Contains(cfg.SnapshotName, "..") || cfg.SnapshotName == "" {
		return fmt.Errorf("--snapshot-name %q must be a simple filename", cfg.SnapshotName)
	}
	return nil
}

func runDrill(ctx context.Context, cfg openBaoConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}

	fmt.Printf("guardian cozystack openbao drill\n")
	fmt.Printf("namespace=%s statefulset=%s bootstrapSecret=%s mode=%s\n", cfg.Namespace, cfg.StatefulSet, cfg.BootstrapSecret, cfg.Mode)

	if err := runner.run(ctx, "get OpenBao StatefulSet", "get", "statefulset.apps/"+cfg.StatefulSet); err != nil {
		return err
	}
	replicas, err := statefulSetReplicas(ctx, runner, cfg.StatefulSet)
	if err != nil {
		return err
	}

	switch cfg.Mode {
	case "status":
		return printStatus(ctx, runner, cfg, replicas)
	case "init-unseal":
		return initUnseal(ctx, runner, cfg, replicas)
	case "snapshot":
		return snapshot(ctx, runner, cfg, replicas)
	default:
		return fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
}

func statefulSetReplicas(ctx context.Context, runner kubectlRunner, statefulSet string) (int, error) {
	out, err := runner.output(ctx, "OpenBao StatefulSet replicas", "get", "statefulset.apps/"+statefulSet, "-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return 0, err
	}
	replicas, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || replicas <= 0 {
		return 0, fmt.Errorf("parse StatefulSet replicas %q: %w", strings.TrimSpace(out), err)
	}
	return replicas, nil
}

func printStatus(ctx context.Context, runner kubectlRunner, cfg openBaoConfig, replicas int) error {
	for i := 0; i < replicas; i++ {
		pod := podName(cfg.StatefulSet, i)
		if _, err := baoStatusForPod(ctx, runner, pod, true); err != nil {
			return err
		}
	}
	if material, err := readBootstrapMaterial(ctx, runner, cfg.BootstrapSecret); err == nil {
		_ = baoRun(ctx, runner, podName(cfg.StatefulSet, 0), "raft autopilot state", material.RootToken, "operator", "raft", "autopilot", "state")
	} else {
		fmt.Printf("bootstrap secret unavailable; skipping raft autopilot state: %v\n", err)
	}
	return nil
}

func initUnseal(ctx context.Context, runner kubectlRunner, cfg openBaoConfig, replicas int) error {
	firstPod := podName(cfg.StatefulSet, 0)
	status, err := baoStatusForPod(ctx, runner, firstPod, true)
	if err != nil {
		return err
	}

	var material bootstrapMaterial
	if !status.Initialized {
		initOut, err := baoOutputSilent(ctx, runner, firstPod, "operator init", "", "operator", "init", "-key-shares=1", "-key-threshold=1", "-format=json")
		if err != nil {
			return err
		}
		material, err = parseInitResult(initOut)
		if err != nil {
			return err
		}
		if err := applyBootstrapSecret(ctx, runner, cfg.BootstrapSecret, material); err != nil {
			return err
		}
		fmt.Printf("initialized OpenBao and wrote cluster-local bootstrap Secret/%s\n", cfg.BootstrapSecret)
	} else {
		material, err = readBootstrapMaterial(ctx, runner, cfg.BootstrapSecret)
		if err != nil {
			return fmt.Errorf("OpenBao is initialized; read bootstrap secret before unseal: %w", err)
		}
	}

	for i := 0; i < replicas; i++ {
		pod := podName(cfg.StatefulSet, i)
		status, err := baoStatusForPod(ctx, runner, pod, true)
		if err != nil {
			return err
		}
		if !status.Sealed {
			fmt.Printf("pod %s already unsealed\n", pod)
			continue
		}
		if err := baoRun(ctx, runner, pod, "operator unseal", "", "operator", "unseal", material.UnsealKey); err != nil {
			return err
		}
	}
	return printStatus(ctx, runner, cfg, replicas)
}

func snapshot(ctx context.Context, runner kubectlRunner, cfg openBaoConfig, replicas int) error {
	if err := runner.run(ctx, "wait OpenBao StatefulSet Ready", "wait", "--for=jsonpath={.status.readyReplicas}="+strconv.Itoa(replicas), "statefulset.apps/"+cfg.StatefulSet, "--timeout="+cfg.WaitTimeout); err != nil {
		return err
	}
	material, err := readBootstrapMaterial(ctx, runner, cfg.BootstrapSecret)
	if err != nil {
		return fmt.Errorf("read bootstrap secret before snapshot: %w", err)
	}
	if err := printStatus(ctx, runner, cfg, replicas); err != nil {
		return err
	}
	pod := podName(cfg.StatefulSet, 0)
	snapshotPath := filepath.Join("/tmp", cfg.SnapshotName)
	if err := baoRun(ctx, runner, pod, "operator raft snapshot save", material.RootToken, "operator", "raft", "snapshot", "save", snapshotPath); err != nil {
		return err
	}
	if err := runner.run(ctx, "snapshot sha256", "exec", "pod/"+pod, "--", "sh", "-ceu", "test -s "+shellQuote(snapshotPath)+" && sha256sum "+shellQuote(snapshotPath)); err != nil {
		return err
	}
	runner.bestEffort(ctx, "remove pod-local snapshot", "exec", "pod/"+pod, "--", "rm", "-f", snapshotPath)
	fmt.Printf("openbao snapshot drill completed: pod=%s snapshot=%s\n", pod, snapshotPath)
	return nil
}

func podName(statefulSet string, ordinal int) string {
	return fmt.Sprintf("%s-%d", statefulSet, ordinal)
}

func baoStatusForPod(ctx context.Context, runner kubectlRunner, pod string, print bool) (baoStatus, error) {
	out, err := baoOutputAllowExit(ctx, runner, pod, "status", "", "status", "-format=json")
	if err != nil {
		return baoStatus{}, err
	}
	status, err := parseBaoStatus(out)
	if err != nil {
		return baoStatus{}, fmt.Errorf("parse bao status for %s: %w\n%s", pod, err, out)
	}
	if print {
		fmt.Printf("pod=%s initialized=%t sealed=%t\n", pod, status.Initialized, status.Sealed)
	}
	return status, nil
}

func parseBaoStatus(raw string) (baoStatus, error) {
	payload, err := jsonObjectPayload(raw)
	if err != nil {
		return baoStatus{}, err
	}
	var status baoStatus
	if err := json.Unmarshal([]byte(payload), &status); err != nil {
		return baoStatus{}, err
	}
	return status, nil
}

func parseInitResult(raw string) (bootstrapMaterial, error) {
	payload, err := jsonObjectPayload(raw)
	if err != nil {
		return bootstrapMaterial{}, fmt.Errorf("parse bao operator init output: %w", err)
	}
	var result initResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return bootstrapMaterial{}, fmt.Errorf("parse bao operator init output: %w", err)
	}
	if len(result.UnsealKeysB64) != 1 || result.UnsealKeysB64[0] == "" {
		return bootstrapMaterial{}, errors.New("bao operator init did not return exactly one unseal key")
	}
	if result.RootToken == "" {
		return bootstrapMaterial{}, errors.New("bao operator init returned empty root token")
	}
	return bootstrapMaterial{UnsealKey: result.UnsealKeysB64[0], RootToken: result.RootToken}, nil
}

func jsonObjectPayload(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end < start {
		return "", errors.New("output did not contain a JSON object")
	}
	payload := trimmed[start : end+1]
	if !json.Valid([]byte(payload)) {
		return "", errors.New("output did not contain a valid JSON object")
	}
	return payload, nil
}

func applyBootstrapSecret(ctx context.Context, runner kubectlRunner, name string, material bootstrapMaterial) error {
	manifest := bootstrapSecretManifest(runner.namespace, name, material)
	return runner.runWithStdin(ctx, "apply OpenBao bootstrap Secret", manifest, "apply", "-f", "-")
}

func bootstrapSecretManifest(namespace, name string, material bootstrapMaterial) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/part-of: guardian
    guardian.dev/secret-scope: openbao-bootstrap
type: Opaque
data:
  unseal-key: %s
  root-token: %s
`,
		name,
		namespace,
		base64.StdEncoding.EncodeToString([]byte(material.UnsealKey)),
		base64.StdEncoding.EncodeToString([]byte(material.RootToken)),
	)
}

func readBootstrapMaterial(ctx context.Context, runner kubectlRunner, name string) (bootstrapMaterial, error) {
	unsealKey, err := readSecretKey(ctx, runner, name, "unseal-key")
	if err != nil {
		return bootstrapMaterial{}, err
	}
	rootToken, err := readSecretKey(ctx, runner, name, "root-token")
	if err != nil {
		return bootstrapMaterial{}, err
	}
	return bootstrapMaterial{UnsealKey: unsealKey, RootToken: rootToken}, nil
}

func readSecretKey(ctx context.Context, runner kubectlRunner, name, key string) (string, error) {
	raw, err := runner.output(ctx, "read Secret/"+name+" "+key, "get", "secret/"+name, "-o", `go-template={{index .data "`+key+`"}}`)
	if err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("decode Secret/%s %s: %w", name, key, err)
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("Secret/%s %s is empty", name, key)
	}
	return string(decoded), nil
}

func baoRun(ctx context.Context, runner kubectlRunner, pod, label, token string, args ...string) error {
	_, err := baoOutput(ctx, runner, pod, label, token, args...)
	return err
}

func baoOutput(ctx context.Context, runner kubectlRunner, pod, label, token string, args ...string) (string, error) {
	out, err := baoOutputAllowExit(ctx, runner, pod, label, token, args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

func baoOutputSilent(ctx context.Context, runner kubectlRunner, pod, label, token string, args ...string) (string, error) {
	execArgs := baoExecArgs(pod, token, args...)
	out, err := runner.combinedOutput(ctx, execArgs...)
	fmt.Printf("\n## %s on %s\n", label, pod)
	if err != nil {
		return "", fmt.Errorf("%s on %s: %w", label, pod, err)
	}
	fmt.Printf("captured %s output without printing secret material\n", label)
	return out, nil
}

func baoOutputAllowExit(ctx context.Context, runner kubectlRunner, pod, label, token string, args ...string) (string, error) {
	execArgs := baoExecArgs(pod, token, args...)
	out, err := runner.combinedOutput(ctx, execArgs...)
	fmt.Printf("\n## %s on %s\n", label, pod)
	fmt.Print(redactToken(out, token))
	if err != nil && !looksLikeBaoStatusJSON(out) {
		return "", fmt.Errorf("%s on %s: %w", label, pod, err)
	}
	return out, nil
}

func baoExecArgs(pod, token string, args ...string) []string {
	execArgs := []string{"exec", "pod/" + pod, "--", "env", "BAO_ADDR=" + baoAddr, "VAULT_ADDR=" + baoAddr, "VAULT_CLIENT_TIMEOUT=120s"}
	if token != "" {
		execArgs = append(execArgs, "BAO_TOKEN="+token, "VAULT_TOKEN="+token)
	}
	execArgs = append(execArgs, "bao")
	execArgs = append(execArgs, args...)
	return execArgs
}

func looksLikeBaoStatusJSON(out string) bool {
	payload, err := jsonObjectPayload(out)
	if err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return false
	}
	_, hasInitialized := fields["initialized"]
	_, hasSealed := fields["sealed"]
	return hasInitialized && hasSealed
}

func redactToken(out, token string) string {
	if token == "" {
		return out
	}
	return strings.ReplaceAll(out, token, "<redacted>")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	requestTimeout string
	namespace      string
}

func (r kubectlRunner) baseArgs(args ...string) []string {
	out := make([]string, 0, len(args)+6)
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
	return r.runWithStdin(ctx, label, "", args...)
}

func (r kubectlRunner) runWithStdin(ctx context.Context, label string, stdin string, args ...string) error {
	fmt.Printf("\n## %s\n", label)
	cmd := exec.CommandContext(ctx, r.bin, r.baseArgs(args...)...)
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

func (r kubectlRunner) bestEffort(ctx context.Context, label string, args ...string) {
	if err := r.run(ctx, label, args...); err != nil {
		fmt.Printf("best-effort command failed: %v\n", err)
	}
}

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	out, err := r.combinedOutput(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w\n%s", label, err, out)
	}
	return out, nil
}

func (r kubectlRunner) combinedOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.baseArgs(args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
