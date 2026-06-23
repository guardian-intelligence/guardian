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
	"regexp"
	"strings"
	"time"
)

type drillConfig struct {
	Kubectl                string
	Kubeconfig             string
	RequestTimeout         string
	DrainTimeout           string
	WaitTimeout            string
	Node                   string
	ConfirmNode            string
	OpenBaoNamespace       string
	OpenBaoApp             string
	OpenBaoStatefulSet     string
	OpenBaoBootstrapSecret string
}

type kubectlCheck struct {
	Label string
	Args  []string
}

type statefulSetReplicas struct {
	Replicas      int
	ReadyReplicas int
}

type baoStatus struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
}

const baoAddr = "http://127.0.0.1:8200"

var dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func main() {
	var cfg drillConfig
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.DrainTimeout, "drain-timeout", "10m", "kubectl drain timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "15m", "post-recovery readiness wait timeout")
	flag.StringVar(&cfg.Node, "node", "", "Kubernetes node name to cordon and drain")
	flag.StringVar(&cfg.ConfirmNode, "confirm-node", "", "must exactly match --node before the drill mutates the cluster")
	flag.StringVar(&cfg.OpenBaoNamespace, "openbao-namespace", "tenant-root", "OpenBao namespace")
	flag.StringVar(&cfg.OpenBaoApp, "openbao-app", "guardian", "OpenBao Cozystack app name")
	flag.StringVar(&cfg.OpenBaoStatefulSet, "openbao-statefulset", "openbao-guardian", "OpenBao StatefulSet name")
	flag.StringVar(&cfg.OpenBaoBootstrapSecret, "openbao-bootstrap-secret", "openbao-guardian-bootstrap", "Kubernetes Secret for cluster-local OpenBao bootstrap material")
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

func validateConfig(cfg drillConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if err := validateNodeName(cfg.Node); err != nil {
		return err
	}
	if cfg.ConfirmNode != cfg.Node {
		return errors.New("--confirm-node must exactly match --node before running this disruptive drill")
	}
	for label, value := range map[string]string{
		"request-timeout": cfg.RequestTimeout,
		"drain-timeout":   cfg.DrainTimeout,
		"wait-timeout":    cfg.WaitTimeout,
	} {
		if value == "" {
			return fmt.Errorf("--%s must not be empty", label)
		}
	}
	for label, value := range map[string]string{
		"openbao-namespace":        cfg.OpenBaoNamespace,
		"openbao-app":              cfg.OpenBaoApp,
		"openbao-statefulset":      cfg.OpenBaoStatefulSet,
		"openbao-bootstrap-secret": cfg.OpenBaoBootstrapSecret,
	} {
		if !dnsSubdomainRE.MatchString(value) {
			return fmt.Errorf("--%s %q is not a Kubernetes DNS subdomain", label, value)
		}
	}
	return nil
}

func validateNodeName(name string) error {
	if name == "" {
		return errors.New("--node is required")
	}
	if len(name) > 253 {
		return fmt.Errorf("--node %q is %d bytes; Kubernetes node names are limited to 253", name, len(name))
	}
	if !dnsSubdomainRE.MatchString(name) {
		return fmt.Errorf("--node %q is not a Kubernetes DNS subdomain", name)
	}
	return nil
}

func runDrill(ctx context.Context, cfg drillConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		requestTimeout: cfg.RequestTimeout,
	}

	fmt.Printf("guardian cozystack node outage drill\n")
	fmt.Printf("node=%s drainTimeout=%s waitTimeout=%s\n", cfg.Node, cfg.DrainTimeout, cfg.WaitTimeout)

	if err := runner.run(ctx, "preflight target node", "get", "node/"+cfg.Node, "-o", "wide"); err != nil {
		return err
	}
	if err := runner.run(ctx, "preflight target node Ready", nodeReadyArgs(cfg.Node, cfg.WaitTimeout)...); err != nil {
		return err
	}
	printStatus(ctx, runner, cfg.Node, "preflight")

	cordoned := false
	defer func() {
		if cordoned {
			runner.bestEffort(ctx, "cleanup uncordon node", "uncordon", cfg.Node)
		}
	}()

	if err := runner.run(ctx, "cordon node", "cordon", cfg.Node); err != nil {
		return err
	}
	cordoned = true

	if err := runner.run(ctx, "drain node", drainArgs(cfg)...); err != nil {
		printStatus(ctx, runner, cfg.Node, "failed-drain")
		return err
	}

	printStatus(ctx, runner, cfg.Node, "drained")

	for _, check := range outageWaits(cfg) {
		if err := runner.run(ctx, check.Label, check.Args...); err != nil {
			printStatus(ctx, runner, cfg.Node, "failed-outage")
			return err
		}
	}
	if err := waitCompanySitePDBsHealthy(ctx, runner, cfg); err != nil {
		printStatus(ctx, runner, cfg.Node, "failed-outage")
		return err
	}
	if err := waitOpenBaoQuorum(ctx, runner, cfg); err != nil {
		printStatus(ctx, runner, cfg.Node, "failed-outage")
		return err
	}
	printStatus(ctx, runner, cfg.Node, "outage-verified")

	if err := runner.run(ctx, "uncordon node", "uncordon", cfg.Node); err != nil {
		return err
	}
	cordoned = false

	if err := runner.run(ctx, "wait recovered target node Ready", nodeReadyArgs(cfg.Node, cfg.WaitTimeout)...); err != nil {
		printStatus(ctx, runner, cfg.Node, "failed-recovery")
		return err
	}
	if err := ensureOpenBaoUnsealed(ctx, runner, cfg); err != nil {
		printStatus(ctx, runner, cfg.Node, "failed-recovery")
		return err
	}
	if err := waitOpenBaoFullReadiness(ctx, runner, cfg); err != nil {
		printStatus(ctx, runner, cfg.Node, "failed-recovery")
		return err
	}
	for _, check := range recoveryServiceWaits(cfg) {
		if err := runner.run(ctx, check.Label, check.Args...); err != nil {
			printStatus(ctx, runner, cfg.Node, "failed-recovery")
			return err
		}
	}

	printStatus(ctx, runner, cfg.Node, "recovered")
	fmt.Printf("node outage drill completed: node=%s\n", cfg.Node)
	return nil
}

func drainArgs(cfg drillConfig) []string {
	return []string{
		"drain",
		cfg.Node,
		"--ignore-daemonsets",
		"--delete-emptydir-data",
		"--timeout=" + cfg.DrainTimeout,
	}
}

func nodeReadyArgs(node, timeout string) []string {
	return []string{"wait", "--for=condition=Ready", "node/" + node, "--timeout=" + timeout}
}

func nodeUnschedulableArgs(node, timeout string) []string {
	return []string{"wait", "--for=jsonpath={.spec.unschedulable}=true", "node/" + node, "--timeout=" + timeout}
}

func printStatus(ctx context.Context, runner kubectlRunner, node, phase string) {
	for _, check := range statusGets(node, phase) {
		runner.bestEffort(ctx, check.Label, check.Args...)
	}
}

func statusGets(node, phase string) []kubectlCheck {
	return []kubectlCheck{
		{
			Label: phase + " nodes",
			Args:  []string{"get", "nodes", "-o", "wide"},
		},
		{
			Label: phase + " pods on target node",
			Args:  []string{"get", "pods", "-A", "--field-selector", "spec.nodeName=" + node, "-o", "wide"},
		},
		{
			Label: phase + " pod disruption budgets",
			Args:  []string{"get", "poddisruptionbudgets.policy", "-A", "-o", "wide"},
		},
		{
			Label: phase + " cozystack stateful apps",
			Args:  []string{"get", "postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io", "-A"},
		},
		{
			Label: phase + " openbao apps",
			Args:  []string{"get", "openbaos.apps.cozystack.io", "-A"},
		},
		{
			Label: phase + " company-site deployments",
			Args:  []string{"get", "deployments.apps", "-A", "-l", "app.kubernetes.io/name=company-site", "-o", "wide"},
		},
		{
			Label: phase + " dashboard deployments",
			Args:  []string{"-n", "cozy-dashboard", "get", "deployments.apps", "-o", "wide"},
		},
	}
}

func outageWaits(cfg drillConfig) []kubectlCheck {
	checks := []kubectlCheck{
		{
			Label: "wait outage target node cordoned",
			Args:  nodeUnschedulableArgs(cfg.Node, cfg.WaitTimeout),
		},
	}
	checks = append(checks, serviceReadinessWaits("outage", cfg, false)...)
	return checks
}

func recoveryWaits(cfg drillConfig) []kubectlCheck {
	checks := []kubectlCheck{
		{
			Label: "wait recovered target node Ready",
			Args:  nodeReadyArgs(cfg.Node, cfg.WaitTimeout),
		},
	}
	checks = append(checks, recoveryServiceWaits(cfg)...)
	return checks
}

func recoveryServiceWaits(cfg drillConfig) []kubectlCheck {
	return serviceReadinessWaits("recovered", cfg, true)
}

func serviceReadinessWaits(phase string, cfg drillConfig, includeCompanySiteDeployments bool) []kubectlCheck {
	checks := []kubectlCheck{
		{
			Label: "wait " + phase + " dashboard console deployment",
			Args:  []string{"-n", "cozy-dashboard", "wait", "--for=condition=Available", "deployment/cozy-dashboard-console"},
		},
		{
			Label: "wait " + phase + " dashboard gatekeeper deployment",
			Args:  []string{"-n", "cozy-dashboard", "wait", "--for=condition=Available", "deployment/incloud-web-gatekeeper"},
		},
		{
			Label: "wait " + phase + " root openbao app",
			Args:  []string{"-n", cfg.OpenBaoNamespace, "wait", "--for=condition=Ready", "openbaos.apps.cozystack.io/" + cfg.OpenBaoApp},
		},
	}
	for _, namespace := range []string{"tenant-root", "tenant-guardiancommercial-platform-dev", "tenant-guardiancommercial-platform-gamma", "tenant-guardiancommercial-platform-prod"} {
		label := namespace
		registry := "harbor-guardian-registry"
		checks = append(checks,
			kubectlCheck{
				Label: "wait " + phase + " " + label + " postgres app",
				Args:  []string{"-n", namespace, "wait", "--for=condition=Ready", "postgreses.apps.cozystack.io/guardian"},
			},
			kubectlCheck{
				Label: "wait " + phase + " " + label + " postgres workloads",
				Args:  []string{"-n", namespace, "wait", "--for=condition=WorkloadsReady", "postgreses.apps.cozystack.io/guardian"},
			},
			kubectlCheck{
				Label: "wait " + phase + " " + label + " harbor app",
				Args:  []string{"-n", namespace, "wait", "--for=condition=Ready", "harbors.apps.cozystack.io/guardian"},
			},
			kubectlCheck{
				Label: "wait " + phase + " " + label + " harbor registry bucket ready",
				Args:  []string{"-n", namespace, "wait", "--for=jsonpath={.status.bucketReady}=true", "bucketclaims.objectstorage.k8s.io/" + registry},
			},
			kubectlCheck{
				Label: "wait " + phase + " " + label + " harbor registry bucket access granted",
				Args:  []string{"-n", namespace, "wait", "--for=jsonpath={.status.accessGranted}=true", "bucketaccesses.objectstorage.k8s.io/" + registry},
			},
			kubectlCheck{
				Label: "wait " + phase + " " + label + " harbor workloads",
				Args:  []string{"-n", namespace, "wait", "--for=condition=WorkloadsReady", "harbors.apps.cozystack.io/guardian"},
			},
			kubectlCheck{
				Label: "wait " + phase + " " + label + " clickhouse app",
				Args:  []string{"-n", namespace, "wait", "--for=condition=Ready", "clickhouses.apps.cozystack.io/guardian"},
			},
			kubectlCheck{
				Label: "wait " + phase + " " + label + " clickhouse workloads",
				Args:  []string{"-n", namespace, "wait", "--for=condition=WorkloadsReady", "clickhouses.apps.cozystack.io/guardian"},
			},
		)
	}
	if includeCompanySiteDeployments {
		for _, namespace := range companySiteNamespaces() {
			checks = append(checks, kubectlCheck{
				Label: "wait " + phase + " " + namespace + " company-site deployment",
				Args:  []string{"-n", namespace, "wait", "--for=condition=Available", "deployment/company-site"},
			})
		}
	}
	for i := range checks {
		checks[i].Args = append(checks[i].Args, "--timeout="+cfg.WaitTimeout)
	}
	return checks
}

func companySiteNamespaces() []string {
	return []string{"tenant-guardiancommercial-platform-dev", "tenant-guardiancommercial-platform-gamma", "tenant-guardiancommercial-platform-prod"}
}

func waitCompanySitePDBsHealthy(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	for _, namespace := range companySiteNamespaces() {
		if err := waitPDBHealthy(ctx, runner, cfg.WaitTimeout, namespace, "company-site", "wait outage "+namespace+" company-site pdb healthy"); err != nil {
			return err
		}
	}
	return nil
}

func waitPDBHealthy(ctx context.Context, runner kubectlRunner, timeoutValue, namespace, name, label string) error {
	return waitJSONStatus(ctx, timeoutValue, label, func(ctx context.Context) (string, bool, error) {
		status, err := pdbHealth(ctx, runner, namespace, name)
		if err != nil {
			return "", false, err
		}
		message := fmt.Sprintf("namespace=%s pdb=%s currentHealthy=%d desiredHealthy=%d disruptionsAllowed=%d",
			namespace,
			name,
			status.CurrentHealthy,
			status.DesiredHealthy,
			status.DisruptionsAllowed,
		)
		return message, status.CurrentHealthy >= status.DesiredHealthy, nil
	})
}

type pdbHealthStatus struct {
	CurrentHealthy     int
	DesiredHealthy     int
	DisruptionsAllowed int
}

func pdbHealth(ctx context.Context, runner kubectlRunner, namespace, name string) (pdbHealthStatus, error) {
	out, err := runner.output(ctx, "get PodDisruptionBudget health", "-n", namespace, "get", "poddisruptionbudgets.policy/"+name, "-o", "json")
	if err != nil {
		return pdbHealthStatus{}, err
	}
	return parsePDBHealth(out)
}

func parsePDBHealth(raw string) (pdbHealthStatus, error) {
	var payload struct {
		Status struct {
			CurrentHealthy     int `json:"currentHealthy"`
			DesiredHealthy     int `json:"desiredHealthy"`
			DisruptionsAllowed int `json:"disruptionsAllowed"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return pdbHealthStatus{}, fmt.Errorf("parse PodDisruptionBudget JSON: %w", err)
	}
	if payload.Status.DesiredHealthy <= 0 {
		return pdbHealthStatus{}, fmt.Errorf("PodDisruptionBudget desiredHealthy must be positive, got %d", payload.Status.DesiredHealthy)
	}
	return pdbHealthStatus{
		CurrentHealthy:     payload.Status.CurrentHealthy,
		DesiredHealthy:     payload.Status.DesiredHealthy,
		DisruptionsAllowed: payload.Status.DisruptionsAllowed,
	}, nil
}

func waitOpenBaoQuorum(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	return waitOpenBaoReplicas(ctx, runner, cfg, "wait outage root openbao statefulset quorum", quorumForReplicas)
}

func waitOpenBaoFullReadiness(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	return waitOpenBaoReplicas(ctx, runner, cfg, "wait recovered root openbao statefulset full readiness", func(replicas int) int {
		if replicas <= 0 {
			return 1
		}
		return replicas
	})
}

func waitOpenBaoReplicas(ctx context.Context, runner kubectlRunner, cfg drillConfig, label string, requiredReadyReplicas func(int) int) error {
	return waitJSONStatus(ctx, cfg.WaitTimeout, label, func(ctx context.Context) (string, bool, error) {
		status, err := openBaoStatefulSetReplicas(ctx, runner, cfg)
		if err != nil {
			return "", false, err
		}
		required := requiredReadyReplicas(status.Replicas)
		message := fmt.Sprintf("namespace=%s statefulset=%s replicas=%d readyReplicas=%d requiredReadyReplicas=%d",
			cfg.OpenBaoNamespace,
			cfg.OpenBaoStatefulSet,
			status.Replicas,
			status.ReadyReplicas,
			required,
		)
		return message, status.ReadyReplicas >= required, nil
	})
}

func waitJSONStatus(ctx context.Context, timeoutValue, label string, check func(context.Context) (message string, done bool, err error)) error {
	timeout, err := time.ParseDuration(timeoutValue)
	if err != nil {
		return fmt.Errorf("parse wait timeout %q for %s: %w", timeoutValue, label, err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	fmt.Printf("\n## %s\n", label)
	var lastMessage string
	var lastErr error
	for {
		message, done, err := check(ctx)
		if err == nil {
			lastErr = nil
			lastMessage = message
			fmt.Println(message)
			if done {
				return nil
			}
		} else {
			lastErr = err
			fmt.Printf("%s poll failed: %v\n", label, err)
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("%s: %w", label, lastErr)
			}
			return fmt.Errorf("%s timed out: %s", label, lastMessage)
		case <-ticker.C:
		}
	}
}

func ensureOpenBaoUnsealed(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	status, err := openBaoStatefulSetReplicas(ctx, runner, cfg)
	if err != nil {
		return err
	}
	unsealKey, err := readOpenBaoUnsealKey(ctx, runner, cfg)
	if err != nil {
		return fmt.Errorf("read OpenBao unseal key before recovery unseal: %w", err)
	}
	for i := 0; i < status.Replicas; i++ {
		pod := podName(cfg.OpenBaoStatefulSet, i)
		if err := waitPodContainerRunning(ctx, runner, cfg.WaitTimeout, cfg.OpenBaoNamespace, pod, "openbao", "wait recovered "+pod+" openbao container running"); err != nil {
			return err
		}
		podStatus, err := baoStatusForPod(ctx, runner, cfg.OpenBaoNamespace, pod, true)
		if err != nil {
			return err
		}
		if !podStatus.Initialized {
			return fmt.Errorf("OpenBao pod %s is not initialized", pod)
		}
		if !podStatus.Sealed {
			fmt.Printf("pod %s already unsealed\n", pod)
			continue
		}
		if err := baoRun(ctx, runner, cfg.OpenBaoNamespace, pod, "operator unseal", "", "operator", "unseal", unsealKey); err != nil {
			return err
		}
	}
	return nil
}

func waitPodContainerRunning(ctx context.Context, runner kubectlRunner, timeoutValue, namespace, pod, container, label string) error {
	return waitJSONStatus(ctx, timeoutValue, label, func(ctx context.Context) (string, bool, error) {
		running, err := podContainerRunning(ctx, runner, namespace, pod, container)
		if err != nil {
			return "", false, err
		}
		message := fmt.Sprintf("namespace=%s pod=%s container=%s running=%t", namespace, pod, container, running)
		return message, running, nil
	})
}

func podContainerRunning(ctx context.Context, runner kubectlRunner, namespace, pod, container string) (bool, error) {
	out, err := runner.output(ctx, "get pod container status", "-n", namespace, "get", "pod/"+pod, "-o", "json")
	if err != nil {
		return false, err
	}
	return parsePodContainerRunning(out, container)
}

func parsePodContainerRunning(raw, container string) (bool, error) {
	var payload struct {
		Status struct {
			ContainerStatuses []struct {
				Name  string `json:"name"`
				State struct {
					Running *struct{} `json:"running"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false, fmt.Errorf("parse Pod JSON: %w", err)
	}
	for _, status := range payload.Status.ContainerStatuses {
		if status.Name == container {
			return status.State.Running != nil, nil
		}
	}
	return false, nil
}

func openBaoStatefulSetReplicas(ctx context.Context, runner kubectlRunner, cfg drillConfig) (statefulSetReplicas, error) {
	out, err := runner.output(ctx, "get OpenBao StatefulSet replicas", "-n", cfg.OpenBaoNamespace, "get", "statefulset.apps/"+cfg.OpenBaoStatefulSet, "-o", "json")
	if err != nil {
		return statefulSetReplicas{}, err
	}
	return parseStatefulSetReplicas(out)
}

func parseStatefulSetReplicas(raw string) (statefulSetReplicas, error) {
	var payload struct {
		Spec struct {
			Replicas *int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return statefulSetReplicas{}, fmt.Errorf("parse StatefulSet JSON: %w", err)
	}
	replicas := 1
	if payload.Spec.Replicas != nil {
		replicas = *payload.Spec.Replicas
	}
	if replicas <= 0 {
		return statefulSetReplicas{}, fmt.Errorf("StatefulSet replicas must be positive, got %d", replicas)
	}
	return statefulSetReplicas{
		Replicas:      replicas,
		ReadyReplicas: payload.Status.ReadyReplicas,
	}, nil
}

func quorumForReplicas(replicas int) int {
	if replicas <= 0 {
		return 1
	}
	return replicas/2 + 1
}

func podName(statefulSet string, ordinal int) string {
	return fmt.Sprintf("%s-%d", statefulSet, ordinal)
}

func readOpenBaoUnsealKey(ctx context.Context, runner kubectlRunner, cfg drillConfig) (string, error) {
	unsealKey, err := readSecretKey(ctx, runner, cfg.OpenBaoNamespace, cfg.OpenBaoBootstrapSecret, "unseal-key")
	if err != nil {
		return "", err
	}
	return unsealKey, nil
}

func readSecretKey(ctx context.Context, runner kubectlRunner, namespace, name, key string) (string, error) {
	raw, err := runner.output(ctx, "read Secret/"+name+" "+key, "-n", namespace, "get", "secret/"+name, "-o", `go-template={{index .data "`+key+`"}}`)
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

func baoStatusForPod(ctx context.Context, runner kubectlRunner, namespace, pod string, print bool) (baoStatus, error) {
	out, err := baoOutputAllowExit(ctx, runner, namespace, pod, "status", "", "status", "-format=json")
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

func baoRun(ctx context.Context, runner kubectlRunner, namespace, pod, label, token string, args ...string) error {
	_, err := baoOutput(ctx, runner, namespace, pod, label, token, args...)
	return err
}

func baoOutput(ctx context.Context, runner kubectlRunner, namespace, pod, label, token string, args ...string) (string, error) {
	out, err := baoOutputAllowExit(ctx, runner, namespace, pod, label, token, args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

func baoOutputAllowExit(ctx context.Context, runner kubectlRunner, namespace, pod, label, token string, args ...string) (string, error) {
	execArgs := baoExecArgs(namespace, pod, token, args...)
	out, err := runner.combinedOutput(ctx, execArgs...)
	fmt.Printf("\n## %s on %s\n", label, pod)
	fmt.Print(redactToken(out, token))
	if err != nil && !looksLikeBaoStatusJSON(out) {
		return "", fmt.Errorf("%s on %s: %w", label, pod, err)
	}
	return out, nil
}

func baoExecArgs(namespace, pod, token string, args ...string) []string {
	execArgs := []string{"-n", namespace, "exec", "pod/" + pod, "--", "env", "BAO_ADDR=" + baoAddr, "VAULT_ADDR=" + baoAddr, "VAULT_CLIENT_TIMEOUT=120s"}
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

func redactToken(out, token string) string {
	if token == "" {
		return out
	}
	return strings.ReplaceAll(out, token, "<redacted>")
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	requestTimeout string
}

func (r kubectlRunner) baseArgs(args ...string) []string {
	out := make([]string, 0, len(args)+4)
	if r.kubeconfig != "" {
		out = append(out, "--kubeconfig", r.kubeconfig)
	}
	if r.requestTimeout != "" {
		out = append(out, "--request-timeout="+r.requestTimeout)
	}
	out = append(out, args...)
	return out
}

func (r kubectlRunner) run(ctx context.Context, label string, args ...string) error {
	fmt.Printf("\n## %s\n", label)
	out, err := r.combinedOutput(ctx, args...)
	fmt.Print(out)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func (r kubectlRunner) bestEffort(ctx context.Context, label string, args ...string) {
	fmt.Printf("\n## %s\n", label)
	out, err := r.combinedOutput(ctx, args...)
	fmt.Print(out)
	if err != nil {
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
