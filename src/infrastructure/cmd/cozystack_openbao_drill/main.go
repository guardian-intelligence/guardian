package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// The 8200 listener serves the cert-manager-issued TLS cert. Verification is
// skipped for this localhost status probe: the exec already crossed the pod
// boundary, and listener transport identity is asserted by the converged
// proof's Certificate checks.
const baoAddr = "https://127.0.0.1:8200"

type openBaoConfig struct {
	Kubectl        string
	Kubeconfig     string
	KubeAPIServer  string
	RequestTimeout string
	Namespace      string
	StatefulSet    string
}

type baoStatus struct {
	Initialized bool   `json:"initialized"`
	Sealed      bool   `json:"sealed"`
	HAEnabled   bool   `json:"ha_enabled"`
	ClusterID   string `json:"cluster_id"`
	Version     string `json:"version"`
}

type podBaoStatus struct {
	Pod    string
	Status baoStatus
}

var dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func main() {
	var cfg openBaoConfig
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API server override for off-VLAN proof runs")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-guardian", "OpenBao namespace")
	flag.StringVar(&cfg.StatefulSet, "statefulset", "guardian-openbao", "OpenBao StatefulSet name")
	flag.Parse()

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

func validateConfig(cfg openBaoConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	for label, value := range map[string]string{
		"namespace":   cfg.Namespace,
		"statefulset": cfg.StatefulSet,
	} {
		if len(value) > 253 {
			return fmt.Errorf("--%s %q is longer than a Kubernetes DNS subdomain", label, value)
		}
		if !dnsSubdomainRE.MatchString(value) {
			return fmt.Errorf("--%s %q is not a Kubernetes DNS subdomain", label, value)
		}
	}
	return nil
}

func runDrill(ctx context.Context, cfg openBaoConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		kubeAPIServer:  cfg.KubeAPIServer,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}

	fmt.Printf("guardian cozystack openbao drill\n")
	fmt.Printf("namespace=%s statefulset=%s\n", cfg.Namespace, cfg.StatefulSet)

	if err := runner.run(ctx, "get OpenBao StatefulSet", "get", "statefulset.apps/"+cfg.StatefulSet); err != nil {
		return err
	}
	replicas, err := statefulSetReplicas(ctx, runner, cfg.StatefulSet)
	if err != nil {
		return err
	}
	expectedVersion, err := expectedOpenBaoVersion(ctx, runner, cfg.StatefulSet)
	if err != nil {
		return err
	}
	fmt.Printf("expected OpenBao version from StatefulSet template: %s\n", expectedVersion)

	return printStatus(ctx, runner, cfg, replicas, expectedVersion)
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

func expectedOpenBaoVersion(ctx context.Context, runner kubectlRunner, statefulSet string) (string, error) {
	image, err := runner.output(ctx, "OpenBao StatefulSet template image", "get", "statefulset.apps/"+statefulSet, "-o", "jsonpath={.spec.template.spec.containers[?(@.name==\"openbao\")].image}")
	if err != nil {
		return "", err
	}
	version, err := openBaoVersionFromImage(strings.TrimSpace(image))
	if err != nil {
		return "", fmt.Errorf("parse OpenBao version from StatefulSet image %q: %w", strings.TrimSpace(image), err)
	}
	return version, nil
}

func openBaoVersionFromImage(image string) (string, error) {
	if image == "" {
		return "", errors.New("empty image")
	}
	nameAndTag := image
	if digestStart := strings.Index(nameAndTag, "@"); digestStart >= 0 {
		nameAndTag = nameAndTag[:digestStart]
	}
	tagStart := strings.LastIndex(nameAndTag, ":")
	if tagStart == -1 || tagStart == len(nameAndTag)-1 {
		return "", errors.New("image has no tag")
	}
	tag := nameAndTag[tagStart+1:]
	if tag == "" {
		return "", errors.New("image tag is empty")
	}
	return tag, nil
}

func printStatus(ctx context.Context, runner kubectlRunner, cfg openBaoConfig, replicas int, expectedVersion string) error {
	statuses := make([]podBaoStatus, 0, replicas)
	for i := 0; i < replicas; i++ {
		pod := podName(cfg.StatefulSet, i)
		status, err := baoStatusForPod(ctx, runner, pod, true)
		if err != nil {
			return err
		}
		statuses = append(statuses, podBaoStatus{Pod: pod, Status: status})
	}
	if err := validateStatusSet(statuses, expectedVersion); err != nil {
		return err
	}
	return nil
}

func podName(statefulSet string, ordinal int) string {
	return fmt.Sprintf("%s-%d", statefulSet, ordinal)
}

func baoStatusForPod(ctx context.Context, runner kubectlRunner, pod string, print bool) (baoStatus, error) {
	out, err := baoOutputAllowExit(ctx, runner, pod, "status", "status", "-format=json")
	if err != nil {
		return baoStatus{}, err
	}
	status, err := parseBaoStatus(out)
	if err != nil {
		return baoStatus{}, fmt.Errorf("parse bao status for %s: %w\n%s", pod, err, out)
	}
	if print {
		fmt.Printf("pod=%s initialized=%t sealed=%t ha_enabled=%t cluster_id=%s version=%s\n", pod, status.Initialized, status.Sealed, status.HAEnabled, status.ClusterID, status.Version)
	}
	return status, nil
}

func validateStatusSet(statuses []podBaoStatus, expectedVersion string) error {
	if len(statuses) == 0 {
		return errors.New("OpenBao status drill found no pods")
	}
	if expectedVersion == "" {
		return errors.New("OpenBao status drill has empty expected version")
	}
	var problems []string
	var clusterID string
	for _, item := range statuses {
		status := item.Status
		if !status.Initialized {
			problems = append(problems, fmt.Sprintf("%s is not initialized", item.Pod))
		}
		if status.Sealed {
			problems = append(problems, fmt.Sprintf("%s is sealed", item.Pod))
		}
		if !status.HAEnabled {
			problems = append(problems, fmt.Sprintf("%s reports ha_enabled=false", item.Pod))
		}
		if status.ClusterID == "" {
			problems = append(problems, fmt.Sprintf("%s reported empty cluster_id", item.Pod))
		} else if clusterID == "" {
			clusterID = status.ClusterID
		} else if status.ClusterID != clusterID {
			problems = append(problems, fmt.Sprintf("%s reports cluster_id=%s; expected %s", item.Pod, status.ClusterID, clusterID))
		}
		if status.Version == "" {
			problems = append(problems, fmt.Sprintf("%s reported empty OpenBao version", item.Pod))
			continue
		}
		if status.Version != expectedVersion {
			problems = append(problems, fmt.Sprintf("%s reports version %s; expected %s", item.Pod, status.Version, expectedVersion))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("OpenBao status drill failed: %s", strings.Join(problems, "; "))
	}
	fmt.Printf("OpenBao raft membership verified: pods=%d cluster_id=%s\n", len(statuses), clusterID)
	return nil
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

func baoOutputAllowExit(ctx context.Context, runner kubectlRunner, pod, label string, args ...string) (string, error) {
	execArgs := baoExecArgs(pod, args...)
	out, err := runner.combinedOutput(ctx, execArgs...)
	fmt.Printf("\n## %s on %s\n", label, pod)
	fmt.Print(out)
	if err != nil && !looksLikeBaoStatusJSON(out) {
		return "", fmt.Errorf("%s on %s: %w", label, pod, err)
	}
	return out, nil
}

func baoExecArgs(pod string, args ...string) []string {
	execArgs := []string{"exec", "pod/" + pod, "-c", "openbao", "--", "env", "BAO_ADDR=" + baoAddr, "VAULT_ADDR=" + baoAddr, "BAO_SKIP_VERIFY=true", "VAULT_SKIP_VERIFY=true", "VAULT_CLIENT_TIMEOUT=120s"}
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

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	kubeAPIServer  string
	requestTimeout string
	namespace      string
}

func (r kubectlRunner) baseArgs(args ...string) []string {
	out := make([]string, 0, len(args)+6)
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
