package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

type drillConfig struct {
	Kubectl        string
	Kubeconfig     string
	RequestTimeout string
	DrainTimeout   string
	WaitTimeout    string
	Node           string
	ConfirmNode    string
}

type kubectlCheck struct {
	Label string
	Args  []string
}

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

	for _, check := range outageWaits(cfg.Node, cfg.WaitTimeout) {
		if err := runner.run(ctx, check.Label, check.Args...); err != nil {
			printStatus(ctx, runner, cfg.Node, "failed-outage")
			return err
		}
	}
	printStatus(ctx, runner, cfg.Node, "outage-verified")

	if err := runner.run(ctx, "uncordon node", "uncordon", cfg.Node); err != nil {
		return err
	}
	cordoned = false

	for _, check := range recoveryWaits(cfg.Node, cfg.WaitTimeout) {
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

func outageWaits(node, timeout string) []kubectlCheck {
	checks := []kubectlCheck{
		{
			Label: "wait outage target node cordoned",
			Args:  nodeUnschedulableArgs(node, timeout),
		},
	}
	checks = append(checks, serviceReadinessWaits("outage", timeout)...)
	return checks
}

func recoveryWaits(node, timeout string) []kubectlCheck {
	checks := []kubectlCheck{
		{
			Label: "wait recovered target node Ready",
			Args:  nodeReadyArgs(node, timeout),
		},
	}
	checks = append(checks, serviceReadinessWaits("recovered", timeout)...)
	return checks
}

func serviceReadinessWaits(phase, timeout string) []kubectlCheck {
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
			Args:  []string{"-n", "tenant-root", "wait", "--for=condition=Ready", "openbaos.apps.cozystack.io/guardian"},
		},
		{
			Label: "wait " + phase + " root openbao statefulset",
			Args:  []string{"-n", "tenant-root", "wait", "--for=jsonpath={.status.readyReplicas}=3", "statefulset.apps/openbao-guardian"},
		},
	}
	for _, namespace := range []string{"tenant-root", "tenant-dev", "tenant-gamma", "tenant-prod"} {
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
	for _, namespace := range []string{"tenant-dev", "tenant-gamma", "tenant-prod"} {
		checks = append(checks, kubectlCheck{
			Label: "wait " + phase + " " + namespace + " company-site deployment",
			Args:  []string{"-n", namespace, "wait", "--for=condition=Available", "deployment/company-site"},
		})
	}
	for i := range checks {
		checks[i].Args = append(checks[i].Args, "--timeout="+timeout)
	}
	return checks
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

func (r kubectlRunner) combinedOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.baseArgs(args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
