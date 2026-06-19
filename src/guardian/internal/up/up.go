package up

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/genesis"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/state"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/toolrunner"
	"gopkg.in/yaml.v3"
)

type Tools struct {
	Talm        string
	Talos       string
	Kubectl     string
	Helm        string
	BootToTalos string
}

type Options struct {
	Execute        bool
	Now            func() time.Time
	WaitForTalos   func(context.Context, string, time.Duration) error
	RunBootToTalos func(context.Context, config.Config, string) error
	Status         StatusReporter
}

type Result struct {
	Outcome       string               `json:"outcome" yaml:"outcome" toml:"outcome"`
	Code          string               `json:"code,omitempty" yaml:"code,omitempty" toml:"code,omitempty"`
	SourcePath    string               `json:"sourcePath,omitempty" yaml:"sourcePath,omitempty" toml:"sourcePath,omitempty"`
	ClusterName   string               `json:"clusterName,omitempty" yaml:"clusterName,omitempty" toml:"clusterName,omitempty"`
	Target        string               `json:"target,omitempty" yaml:"target,omitempty" toml:"target,omitempty"`
	ConfigDigest  string               `json:"configDigest,omitempty" yaml:"configDigest,omitempty" toml:"configDigest,omitempty"`
	StateDir      string               `json:"stateDir,omitempty" yaml:"stateDir,omitempty" toml:"stateDir,omitempty"`
	Kubeconfig    string               `json:"kubeconfig,omitempty" yaml:"kubeconfig,omitempty" toml:"kubeconfig,omitempty"`
	GenesisBundle string               `json:"genesisBundle,omitempty" yaml:"genesisBundle,omitempty" toml:"genesisBundle,omitempty"`
	Stages        []string             `json:"stages,omitempty" yaml:"stages,omitempty" toml:"stages,omitempty"`
	Commands      []toolrunner.Command `json:"commands,omitempty" yaml:"commands,omitempty" toml:"commands,omitempty"`
}

var (
	idempotencyProbeTimeout = 2 * time.Second
	retryAttemptTimeout     = 20 * time.Second
)

func (r Result) Text(w io.Writer) error {
	source := displayPath(r.SourcePath)
	fmt.Fprintf(w, "outcome\t%s\n", r.Outcome)
	if r.Code != "" {
		fmt.Fprintf(w, "code\t%s\n", r.Code)
	}
	if source != "" {
		fmt.Fprintf(w, "source\t%s\n", source)
	}
	if r.ClusterName != "" {
		fmt.Fprintf(w, "cluster\t%s\n", r.ClusterName)
	}
	if r.Target != "" {
		fmt.Fprintf(w, "target\t%s\n", r.Target)
	}
	if r.StateDir != "" {
		fmt.Fprintf(w, "state\t%s\n", r.StateDir)
	}
	if r.Outcome != "Planned" {
		if r.Kubeconfig != "" {
			fmt.Fprintf(w, "kubeconfig\t%s\n", r.Kubeconfig)
		}
		if r.GenesisBundle != "" {
			fmt.Fprintf(w, "genesisBundle\t%s\n", r.GenesisBundle)
		}
	}
	if r.Outcome == "Planned" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "will")
		for _, step := range []string{
			"prepare local bootstrap state",
			"render Talos bootstrap material",
			"install Talos from the stock Ubuntu target",
			"bootstrap Kubernetes with Talm",
			"hand off to the pinned Cozystack installer",
		} {
			fmt.Fprintf(w, "  - %s\n", step)
		}
	}
	return nil
}

func displayPath(path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return path
	}
	return filepath.ToSlash(rel)
}

func Run(ctx context.Context, loaded *config.Loaded, tools Tools, runner toolrunner.Runner, opts Options) Result {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.WaitForTalos == nil {
		opts.WaitForTalos = toolrunner.WaitTCP
	}
	if opts.RunBootToTalos == nil {
		opts.RunBootToTalos = runBootToTalosOnTarget
	}
	reportStatus(opts.Status, openStateStep, StatusRunning, opts.Now, nil)
	layout, err := state.Open(loaded.Config.Cluster.Name)
	if err != nil {
		failure := failureFor("state.open", nil)
		reportStatus(opts.Status, openStateStep, StatusFailed, opts.Now, failure)
		return failed(loaded, nil, "NeedsConfig", failure.Code)
	}
	reportStatus(opts.Status, openStateStep, StatusDone, opts.Now, nil)
	result := Result{
		Outcome:       "Planned",
		SourcePath:    loaded.Path,
		ClusterName:   loaded.Config.Cluster.Name,
		Target:        loaded.Config.Node.Address,
		ConfigDigest:  loaded.Digest,
		StateDir:      layout.Root,
		Kubeconfig:    layout.Kubeconfig,
		GenesisBundle: layout.GenesisArchive,
		Stages: []string{
			"OpenState",
			"TalmInit",
			"WriteTalmValues",
			"BootToTalosInstall",
			"WaitTalosMaintenanceAPI",
			"TalmTemplate",
			"TalmDryRun",
			"TalmApply",
			"WaitTalosAPI",
			"TalmBootstrap",
			"TalmKubeconfig",
			"WaitKubernetesAPI",
			"WaitKubernetesNodeRegistered",
			"WriteCozystackPlatform",
			"WriteGenesisBundle",
			"RemoveControlPlaneTaint",
			"InstallCozystackOperator",
			"WaitCozystackOperator",
			"ApplyCozystackPlatform",
			"WaitCozystackPlatform",
			"WaitNodeReady",
			"WaitCozystackHelmReleases",
		},
	}
	commands := planCommands(loaded.Config, layout, tools)
	result.Commands = commands
	if !opts.Execute {
		return result
	}
	reportStatus(opts.Status, safetyStep, StatusRunning, opts.Now, nil)
	if !loaded.Config.Bootstrap.Destructive || !loaded.Config.Bootstrap.RequireMaintenance {
		reportStatus(opts.Status, safetyStep, StatusBlocked, opts.Now, failureFor("bootstrap.safety", nil))
		result.Outcome = "Refused"
		result.Code = "bootstrap.safety"
		return result
	}
	if err := genesis.ValidateRecipients(loaded.Config.Bootstrap.Genesis.AgeRecipients); err != nil {
		reportStatus(opts.Status, safetyStep, StatusBlocked, opts.Now, failureFor("bootstrap.genesis.ageRecipients", nil))
		result.Outcome = "Refused"
		result.Code = "bootstrap.genesis.ageRecipients"
		return result
	}
	reportStatus(opts.Status, safetyStep, StatusDone, opts.Now, nil)
	unchangedStages := unchangedStageProbes(ctx, loaded, tools, runner, layout)
	reportedUnchangedStages := map[string]bool{}
	for _, cmd := range commands {
		spec := commandStep(cmd.Name)
		if description, ok := unchangedStages[spec.ParentID]; ok {
			if !reportedUnchangedStages[spec.ParentID] {
				reportStatusDescription(opts.Status, parentStep(spec), StatusUnchanged, description, opts.Now, nil)
				reportedUnchangedStages[spec.ParentID] = true
			}
			continue
		}
		reportStatus(opts.Status, spec, StatusRunning, opts.Now, nil)
		switch cmd.Name {
		case "talm-init":
			if talmStateExists(layout) {
				reportStatusDescription(opts.Status, spec, StatusUnchanged, "Already initialized", opts.Now, nil)
				continue
			}
			if err := runner.Run(ctx, cmd); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "write-talm-values":
			if err := writeTalmValues(loaded.Config, layout.TalmValues); err != nil {
				failure := failureFor("talm.values.write", &cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "talm-template":
			out, err := runner.Output(ctx, cmd)
			if err != nil {
				failure := failureForCommandOutput(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
			if err := state.WriteFile(layout.NodeConfig, out); err != nil {
				failure := failureFor("talos.config.write", &cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "talm-dry-run", "talm-apply":
			if talosConfigured(ctx, runner, tools, layout, loaded.Config.Node.Address) {
				reportStatusDescription(opts.Status, spec, StatusUnchanged, "Already configured", opts.Now, nil)
				continue
			}
			if err := runner.Run(ctx, cmd); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "boot-to-talos-install":
			if err := opts.WaitForTalos(ctx, loaded.Config.Node.Address, 2*time.Second); err == nil {
				reportStatusDescription(opts.Status, spec, StatusUnchanged, "Already installed", opts.Now, nil)
				continue
			}
			if err := opts.RunBootToTalos(ctx, loaded.Config, tools.BootToTalos); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "wait-talos-maintenance-api", "wait-talos-api":
			if err := opts.WaitForTalos(ctx, loaded.Config.Node.Address, 5*time.Minute); err != nil {
				failure := failureFor("talos.api.wait", &cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "write-genesis-bundle":
			if _, err := genesis.WriteEncrypted(genesis.Bundle{
				OutputPath:   layout.GenesisArchive,
				Root:         layout.Root,
				ClusterName:  loaded.Config.Cluster.Name,
				ConfigDigest: loaded.Digest,
				CreatedAt:    opts.Now(),
				Recipients:   loaded.Config.Bootstrap.Genesis.AgeRecipients,
				Files:        genesisFiles(loaded.Config),
			}); err != nil {
				failure := failureFor("genesis.bundle.write", &cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "talm-bootstrap":
			if err := runBootstrapWithRetry(ctx, runner, cmd, 5*time.Minute); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "kubectl-wait-kubernetes-api":
			if err := runOutputWithRetry(ctx, runner, cmd, 15*time.Minute, kubernetesAPIRetryable); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "kubectl-wait-node-registered":
			if err := runOutputWithRetry(ctx, runner, cmd, 10*time.Minute, kubernetesNodeRegistrationRetryable); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "kubectl-remove-control-plane-taint":
			if _, err := runner.Output(ctx, cmd); err != nil {
				if taintAlreadyRemoved(err) {
					reportStatusDescription(opts.Status, spec, StatusUnchanged, "Already removed", opts.Now, nil)
					continue
				}
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "helm-install-cozystack":
			if helmReleaseDeployed(ctx, runner, tools, layout.Kubeconfig, loaded.Config.Cozystack.Version) {
				reportStatusDescription(opts.Status, spec, StatusUnchanged, "Already installed", opts.Now, nil)
				continue
			}
			if err := runHelmInstallWithRetry(ctx, runner, cmd, 15*time.Minute); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "write-cozystack-platform":
			if err := writeCozystackPlatform(loaded.Config, layout.CozystackPlatform); err != nil {
				failure := failureFor("cozystack.platform.write", &cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		case "kubectl-wait-cozystack-helmreleases":
			if err := waitCozystackHelmReleases(ctx, runner, cmd, 30*time.Minute); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		default:
			if err := runner.Run(ctx, cmd); err != nil {
				failure := failureForCommand(cmd)
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
				return failed(loaded, layout, "Retryable", failure.Code)
			}
		}
		reportStatus(opts.Status, spec, StatusDone, opts.Now, nil)
		if err := state.WriteOperation(layout.Operation, state.Operation{
			ClusterName:  loaded.Config.Cluster.Name,
			ConfigDigest: loaded.Digest,
			Stage:        cmd.Name,
			UpdatedAt:    opts.Now(),
		}); err != nil {
			failure := failureFor("operation.write", &cmd)
			reportStatus(opts.Status, spec, StatusFailed, opts.Now, failure)
			return failed(loaded, layout, "Retryable", failure.Code)
		}
	}
	result.Outcome = "Converged"
	result.Commands = nil
	return result
}

func unchangedStageProbes(ctx context.Context, loaded *config.Loaded, tools Tools, runner toolrunner.Runner, layout *state.Layout) map[string]string {
	unchanged := map[string]string{}
	if talosConfigured(ctx, runner, tools, layout, loaded.Config.Node.Address) {
		unchanged["talos"] = "Already installed"
	}
	if kubernetesBootstrapped(ctx, runner, tools, layout) {
		unchanged["kubernetes"] = "Already bootstrapped"
	}
	if cozystackConverged(ctx, loaded, tools, runner, layout) {
		unchanged["cozystack"] = "Already converged"
	}
	return unchanged
}

func parentStep(spec StepSpec) StepSpec {
	return StepSpec{
		ID:    spec.ParentID,
		Title: spec.ParentTitle,
	}
}

func planCommands(cfg config.Config, layout *state.Layout, tools Tools) []toolrunner.Command {
	nodeRel := filepath.ToSlash(strings.TrimPrefix(layout.NodeConfig, layout.TalmProject+string(os.PathSeparator)))
	commands := []toolrunner.Command{
		{
			Name: "talm-init",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{
				"init",
				"--preset", cfg.Talm.Preset,
				"--name", cfg.Cluster.Name,
				"--endpoints", cfg.Node.Address,
				"--cluster-endpoint", cfg.Cluster.Endpoint,
				"--image", cfg.Talm.InstallerImage,
				"--talos-version", cfg.Talm.TalosVersion,
				"--root", ".",
				"--force",
			},
		},
		{
			Name: "write-talm-values",
			Bin:  "guardian-internal",
			Args: []string{"talm-values", layout.TalmValues},
		},
		{
			Name: "boot-to-talos-install",
			Bin:  tools.BootToTalos,
			Args: bootToTalosArgs(cfg),
		},
		{
			Name: "wait-talos-maintenance-api",
			Bin:  "guardian-internal",
			Args: []string{"wait-tcp", cfg.Node.Address, "50000"},
		},
		{
			Name: "talm-template",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{
				"template",
				"-e", cfg.Node.Address,
				"--nodes", cfg.Node.Address,
				"-t", cfg.Talm.Template,
				"--talos-version", cfg.Talm.TalosVersion,
				"--kubernetes-version", cfg.Talm.KubernetesVersion,
				"-i",
			},
		},
		{
			Name: "talm-dry-run",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: talmApplyArgs(nodeRel, cfg, "--dry-run"),
		},
		{
			Name: "talm-apply",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: talmApplyArgs(nodeRel, cfg, "--mode", "reboot"),
		},
		{
			Name: "wait-talos-api",
			Bin:  "guardian-internal",
			Args: []string{"wait-tcp", cfg.Node.Address, "50000"},
		},
		{
			Name: "talm-bootstrap",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{"bootstrap", "-f", nodeRel},
		},
		{
			Name: "talm-kubeconfig",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{"kubeconfig", "-f", nodeRel},
		},
		{
			Name: "kubectl-wait-kubernetes-api",
			Bin:  tools.Kubectl,
			Args: kubectlGetArgs(layout.Kubeconfig, "get", "--raw=/readyz"),
		},
		{
			Name: "kubectl-wait-node-registered",
			Bin:  tools.Kubectl,
			Args: kubectlGetArgs(layout.Kubeconfig, "get", "nodes"),
		},
		{
			Name: "write-cozystack-platform",
			Bin:  "guardian-internal",
			Args: []string{"cozystack-platform", layout.CozystackPlatform},
		},
		{
			Name: "write-genesis-bundle",
			Bin:  "guardian-internal",
			Args: []string{"genesis", layout.GenesisArchive},
		},
	}
	if cfg.Cozystack.RemoveControlTaint {
		commands = append(commands, toolrunner.Command{
			Name: "kubectl-remove-control-plane-taint",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "taint", "nodes", "--all", "node-role.kubernetes.io/control-plane-"},
		})
	}
	commands = append(commands,
		toolrunner.Command{
			Name: "helm-install-cozystack",
			Bin:  tools.Helm,
			Args: []string{
				"upgrade", "--install", "cozystack",
				"oci://ghcr.io/cozystack/cozystack/cozy-installer",
				"--version", cfg.Cozystack.Version,
				"--namespace", "cozy-system",
				"--create-namespace",
				"--kubeconfig", layout.Kubeconfig,
			},
		},
		toolrunner.Command{
			Name: "kubectl-wait-cozystack-operator",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "-n", "cozy-system", "rollout", "status", "deploy/cozystack-operator", "--timeout=10m"},
		},
		toolrunner.Command{
			Name: "kubectl-apply-cozystack-platform",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "apply", "-f", layout.CozystackPlatform},
		},
		toolrunner.Command{
			Name: "kubectl-wait-platform-package",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "wait", "package/cozystack.cozystack-platform", "--for=condition=Ready", "--timeout=10m"},
		},
		toolrunner.Command{
			Name: "kubectl-wait-node-ready",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "wait", "nodes", "--all", "--for=condition=Ready", "--timeout=10m"},
		},
		toolrunner.Command{
			Name: "kubectl-wait-cozystack-helmreleases",
			Bin:  tools.Kubectl,
			Args: kubectlGetArgs(layout.Kubeconfig, "get", "hr", "-A", "-o", "json"),
		},
	)
	return commands
}

func kubectlGetArgs(kubeconfig string, args ...string) []string {
	out := []string{"--kubeconfig", kubeconfig, "--request-timeout=10s"}
	return append(out, args...)
}

func talmStateExists(layout *state.Layout) bool {
	for _, path := range []string{
		filepath.Join(layout.TalmProject, "secrets.yaml"),
		filepath.Join(layout.TalmProject, "talm.key"),
	} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func talosConfigured(ctx context.Context, runner toolrunner.Runner, tools Tools, layout *state.Layout, address string) bool {
	_, err := probeOutput(ctx, runner, toolrunner.Command{
		Name: "talos-version",
		Bin:  tools.Talos,
		Args: []string{
			"--talosconfig", layout.Talosconfig,
			"-n", address,
			"-e", address,
			"version",
			"--short",
		},
	})
	return err == nil
}

func kubernetesBootstrapped(ctx context.Context, runner toolrunner.Runner, tools Tools, layout *state.Layout) bool {
	for _, path := range []string{layout.Kubeconfig, layout.GenesisArchive} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	if _, err := probeOutput(ctx, runner, toolrunner.Command{
		Name: "kubectl-probe-kubernetes-api",
		Bin:  tools.Kubectl,
		Args: kubectlGetArgs(layout.Kubeconfig, "get", "--raw=/readyz"),
	}); err != nil {
		return false
	}
	if _, err := probeOutput(ctx, runner, toolrunner.Command{
		Name: "kubectl-probe-node-registered",
		Bin:  tools.Kubectl,
		Args: kubectlGetArgs(layout.Kubeconfig, "get", "nodes"),
	}); err != nil {
		return false
	}
	return true
}

func cozystackConverged(ctx context.Context, loaded *config.Loaded, tools Tools, runner toolrunner.Runner, layout *state.Layout) bool {
	if !operationCompleted(layout.Operation, loaded.Digest, "kubectl-wait-cozystack-helmreleases") {
		return false
	}
	if !helmReleaseDeployed(ctx, runner, tools, layout.Kubeconfig, loaded.Config.Cozystack.Version) {
		return false
	}
	for _, cmd := range []toolrunner.Command{
		{
			Name: "kubectl-probe-cozystack-operator",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "-n", "cozy-system", "rollout", "status", "deploy/cozystack-operator", "--timeout=1s"},
		},
		{
			Name: "kubectl-probe-cozystack-platform",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "wait", "package/cozystack.cozystack-platform", "--for=condition=Ready", "--timeout=1s"},
		},
		{
			Name: "kubectl-probe-node-ready",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "wait", "nodes", "--all", "--for=condition=Ready", "--timeout=1s"},
		},
	} {
		if _, err := probeOutput(ctx, runner, cmd); err != nil {
			return false
		}
	}
	raw, err := probeOutput(ctx, runner, toolrunner.Command{
		Name: "kubectl-probe-cozystack-helmreleases",
		Bin:  tools.Kubectl,
		Args: kubectlGetArgs(layout.Kubeconfig, "get", "hr", "-A", "-o", "json"),
	})
	if err != nil {
		return false
	}
	_, _, requiredReady, requiredTotal, err := countReadyHelmReleases(raw)
	return err == nil && requiredTotal >= 20 && requiredReady == requiredTotal
}

func operationCompleted(path, digest, stage string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var op state.Operation
	if err := json.Unmarshal(raw, &op); err != nil {
		return false
	}
	return op.ConfigDigest == digest && op.Stage == stage
}

type helmReleaseSummary struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Chart  string `json:"chart"`
}

func helmReleaseDeployed(ctx context.Context, runner toolrunner.Runner, tools Tools, kubeconfig, version string) bool {
	raw, err := probeOutput(ctx, runner, toolrunner.Command{
		Name: "helm-probe-cozystack",
		Bin:  tools.Helm,
		Args: []string{"list", "--namespace", "cozy-system", "--kubeconfig", kubeconfig, "--filter", "^cozystack$", "--output", "json"},
	})
	if err != nil {
		return false
	}
	var releases []helmReleaseSummary
	if err := json.Unmarshal(raw, &releases); err != nil {
		return false
	}
	for _, release := range releases {
		if release.Name == "cozystack" &&
			strings.ToLower(release.Status) == "deployed" &&
			strings.Contains(release.Chart, version) {
			return true
		}
	}
	return false
}

func probeOutput(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, idempotencyProbeTimeout)
	defer cancel()
	return runner.Output(probeCtx, cmd)
}

func runBootstrapWithRetry(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout time.Duration) error {
	return runOutputWithRetry(ctx, runner, cmd, timeout, bootstrapRetryable)
}

func runOutputWithRetry(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout time.Duration, retryable func(error) bool) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		_, err := outputWithTimeout(ctx, runner, cmd, retryAttemptTimeout)
		if err == nil {
			return nil
		}
		if bootstrapAlreadyDone(err) {
			return nil
		}
		lastErr = err
		if !retryable(err) || time.Now().After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func runHelmInstallWithRetry(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		err := runWithTimeout(ctx, runner, cmd, timeout)
		if err == nil {
			return nil
		}
		lastErr = err
		if !helmOperationInProgress(err) || time.Now().After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func bootstrapRetryable(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "bootstrap is not available yet") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection error") ||
		strings.Contains(text, "unavailable")
}

func bootstrapAlreadyDone(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "alreadyexists") ||
		strings.Contains(text, "already bootstrapped") ||
		strings.Contains(text, "etcd data directory is not empty")
}

func kubernetesAPIRetryable(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection refused") ||
		strings.Contains(text, "unable to connect") ||
		strings.Contains(text, "the connection to the server") ||
		strings.Contains(text, "server is currently unable") ||
		strings.Contains(text, "service unavailable") ||
		strings.Contains(text, "error from server") ||
		strings.Contains(text, "context deadline exceeded") ||
		strings.Contains(text, "eof") ||
		strings.Contains(text, "i/o timeout") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "timed out")
}

func kubernetesNodeRegistrationRetryable(err error) bool {
	text := strings.ToLower(err.Error())
	return kubernetesAPIRetryable(err) ||
		strings.Contains(text, "notfound") ||
		strings.Contains(text, "not found") ||
		strings.Contains(text, "no resources found")
}

func taintAlreadyRemoved(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "taint \"node-role.kubernetes.io/control-plane\" not found")
}

func helmOperationInProgress(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "another operation") && strings.Contains(text, "in progress")
}

func waitCozystackHelmReleases(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		raw, err := outputWithTimeout(ctx, runner, cmd, retryAttemptTimeout)
		if err != nil {
			lastErr = err
		} else {
			ready, total, requiredReady, requiredTotal, err := countReadyHelmReleases(raw)
			if err == nil && requiredTotal >= 20 && requiredReady == requiredTotal {
				return nil
			}
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("cozystack helmreleases ready %d/%d required %d/%d", ready, total, requiredReady, requiredTotal)
			}
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func outputWithTimeout(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout time.Duration) ([]byte, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runner.Output(attemptCtx, cmd)
}

func runWithTimeout(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout time.Duration) error {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runner.Run(attemptCtx, cmd)
}

type helmReleaseList struct {
	Items []helmReleaseItem `json:"items"`
}

type helmReleaseItem struct {
	Metadata struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"metadata"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func countReadyHelmReleases(raw []byte) (int, int, int, int, error) {
	var list helmReleaseList
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("decode helmreleases: %w", err)
	}
	ready := 0
	requiredReady := 0
	requiredTotal := 0
	for _, item := range list.Items {
		isReady := helmReleaseReady(item)
		if isReady {
			ready++
		}
		if optionalCozystackHelmRelease(item) {
			continue
		}
		requiredTotal++
		if isReady {
			requiredReady++
		}
	}
	return ready, len(list.Items), requiredReady, requiredTotal, nil
}

func helmReleaseReady(item helmReleaseItem) bool {
	for i := len(item.Status.Conditions) - 1; i >= 0; i-- {
		condition := item.Status.Conditions[i]
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func optionalCozystackHelmRelease(item helmReleaseItem) bool {
	switch item.Metadata.Namespace + "/" + item.Metadata.Name {
	case "cozy-dashboard/dashboard", "cozy-fluxcd/flux-plunger":
		return true
	default:
		return false
	}
}

func bootToTalosArgs(cfg config.Config) []string {
	return []string{
		"-mode", "boot",
		"-image", cfg.Talm.InstallerImage,
		"-yes",
	}
}

func talmApplyArgs(nodeRel string, cfg config.Config, extra ...string) []string {
	args := []string{
		"apply",
		"-f", nodeRel,
		"--talos-version", cfg.Talm.TalosVersion,
		"--kubernetes-version", cfg.Talm.KubernetesVersion,
		"-i",
	}
	return append(args, extra...)
}

func genesisFiles(cfg config.Config) []string {
	files := []string{
		"talm/values.yaml",
		"talm/talm.key",
		"talm/secrets.yaml",
		"talm/talosconfig",
		"talm/nodes/controlplane.yaml",
		"talm/kubeconfig",
		"cozystack-platform.yaml",
		"operation.json",
	}
	return files
}

type cozystackPlatformManifest struct {
	APIVersion string                    `yaml:"apiVersion"`
	Kind       string                    `yaml:"kind"`
	Metadata   cozystackPlatformMetadata `yaml:"metadata"`
	Spec       cozystackPlatformSpec     `yaml:"spec"`
}

type cozystackPlatformMetadata struct {
	Name string `yaml:"name"`
}

type cozystackPlatformSpec struct {
	Variant    string                                      `yaml:"variant"`
	Components map[string]cozystackPlatformComponentConfig `yaml:"components"`
}

type cozystackPlatformComponentConfig struct {
	Values cozystackPlatformValues `yaml:"values"`
}

type cozystackPlatformValues struct {
	Publishing cozystackPublishingValues `yaml:"publishing"`
	Networking cozystackNetworkingValues `yaml:"networking"`
	Telemetry  cozystackTelemetryValues  `yaml:"telemetry"`
}

type cozystackPublishingValues struct {
	Host              string   `yaml:"host"`
	APIServerEndpoint string   `yaml:"apiServerEndpoint"`
	ExposedServices   []string `yaml:"exposedServices"`
}

type cozystackNetworkingValues struct {
	PodCIDR     string           `yaml:"podCIDR"`
	PodGateway  string           `yaml:"podGateway"`
	ServiceCIDR string           `yaml:"serviceCIDR"`
	JoinCIDR    string           `yaml:"joinCIDR"`
	KubeOVN     cozystackKubeOVN `yaml:"kubeovn"`
}

type cozystackKubeOVN struct {
	MasterNodes string `yaml:"MASTER_NODES"`
}

type cozystackTelemetryValues struct {
	Enabled bool `yaml:"enabled"`
}

func writeCozystackPlatform(cfg config.Config, path string) error {
	podGateway, err := cidrFirstHost(cfg.Cluster.PodCIDR)
	if err != nil {
		return err
	}
	doc := cozystackPlatformManifest{
		APIVersion: "cozystack.io/v1alpha1",
		Kind:       "Package",
		Metadata: cozystackPlatformMetadata{
			Name: "cozystack.cozystack-platform",
		},
		Spec: cozystackPlatformSpec{
			Variant: cfg.Cozystack.PlatformVariant,
			Components: map[string]cozystackPlatformComponentConfig{
				"platform": {
					Values: cozystackPlatformValues{
						Publishing: cozystackPublishingValues{
							Host:              cfg.Cozystack.PublishingHost,
							APIServerEndpoint: cfg.Cluster.Endpoint,
							ExposedServices:   cfg.Cozystack.ExposedServices,
						},
						Networking: cozystackNetworkingValues{
							PodCIDR:     cfg.Cluster.PodCIDR,
							PodGateway:  podGateway,
							ServiceCIDR: cfg.Cluster.ServiceCIDR,
							JoinCIDR:    cfg.Cluster.JoinCIDR,
							KubeOVN: cozystackKubeOVN{
								MasterNodes: cfg.Node.Address,
							},
						},
						Telemetry: cozystackTelemetryValues{
							Enabled: false,
						},
					},
				},
			},
		},
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode cozystack platform: %w", err)
	}
	return state.WriteFile(path, raw)
}

func cidrFirstHost(cidr string) (string, error) {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("cozystack platform podCIDR: %w", err)
	}
	next := append(net.IP(nil), ip.To4()...)
	if next == nil {
		return "", fmt.Errorf("cozystack platform podCIDR must be IPv4, got %q", cidr)
	}
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next.String(), nil
}

func writeTalmValues(cfg config.Config, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read talm values: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode talm values: %w", err)
	}
	root := documentRoot(&doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return fmt.Errorf("talm values root is not a mapping")
	}
	setStringSequence(root, "advertisedSubnets", []string{cfg.Cluster.AdvertisedCIDR})

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode talm values: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close talm values encoder: %w", err)
	}
	if err := state.WriteFile(path, buf.Bytes()); err != nil {
		return fmt.Errorf("write talm values: %w", err)
	}
	return nil
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setStringSequence(node *yaml.Node, key string, values []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		seq.Content = append(seq.Content, scalarNode(value))
	}
	if existing := mappingValue(node, key); existing != nil {
		existing.Kind = seq.Kind
		existing.Tag = seq.Tag
		existing.Value = ""
		existing.Content = seq.Content
		return
	}
	node.Content = append(node.Content, scalarNode(key), seq)
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func failed(loaded *config.Loaded, layout *state.Layout, outcome, code string) Result {
	stateDir := ""
	kubeconfig := ""
	if layout != nil {
		stateDir = layout.Root
		kubeconfig = layout.Kubeconfig
		genesisBundle := layout.GenesisArchive
		return Result{
			Outcome:       outcome,
			Code:          code,
			SourcePath:    loaded.Path,
			ClusterName:   loaded.Config.Cluster.Name,
			Target:        loaded.Config.Node.Address,
			ConfigDigest:  loaded.Digest,
			StateDir:      stateDir,
			Kubeconfig:    kubeconfig,
			GenesisBundle: genesisBundle,
		}
	}
	return Result{
		Outcome:      outcome,
		Code:         code,
		SourcePath:   loaded.Path,
		ClusterName:  loaded.Config.Cluster.Name,
		Target:       loaded.Config.Node.Address,
		ConfigDigest: loaded.Digest,
		StateDir:     stateDir,
		Kubeconfig:   kubeconfig,
	}
}
