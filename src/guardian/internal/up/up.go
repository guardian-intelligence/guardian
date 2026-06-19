package up

import (
	"context"
	"fmt"
	"io"
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
	Talm    string
	Talos   string
	Kubectl string
	Helm    string
}

type Options struct {
	Execute      bool
	Now          func() time.Time
	WaitForTalos func(context.Context, string, time.Duration) error
	Status       StatusReporter
}

type Result struct {
	Outcome       string               `json:"outcome" yaml:"outcome" toml:"outcome"`
	Reason        string               `json:"reason,omitempty" yaml:"reason,omitempty" toml:"reason,omitempty"`
	ClusterName   string               `json:"clusterName" yaml:"clusterName" toml:"clusterName"`
	ConfigDigest  string               `json:"configDigest" yaml:"configDigest" toml:"configDigest"`
	StateDir      string               `json:"stateDir" yaml:"stateDir" toml:"stateDir"`
	Kubeconfig    string               `json:"kubeconfig,omitempty" yaml:"kubeconfig,omitempty" toml:"kubeconfig,omitempty"`
	GenesisBundle string               `json:"genesisBundle,omitempty" yaml:"genesisBundle,omitempty" toml:"genesisBundle,omitempty"`
	Stages        []string             `json:"stages" yaml:"stages" toml:"stages"`
	Commands      []toolrunner.Command `json:"commands,omitempty" yaml:"commands,omitempty" toml:"commands,omitempty"`
}

func (r Result) Text(w io.Writer) error {
	fmt.Fprintf(w, "outcome\t%s\n", r.Outcome)
	if r.Reason != "" {
		fmt.Fprintf(w, "reason\t%s\n", r.Reason)
	}
	fmt.Fprintf(w, "cluster\t%s\n", r.ClusterName)
	fmt.Fprintf(w, "configDigest\t%s\n", r.ConfigDigest)
	fmt.Fprintf(w, "state\t%s\n", r.StateDir)
	if r.Kubeconfig != "" {
		fmt.Fprintf(w, "kubeconfig\t%s\n", r.Kubeconfig)
	}
	if r.GenesisBundle != "" {
		fmt.Fprintf(w, "genesisBundle\t%s\n", r.GenesisBundle)
	}
	for _, stage := range r.Stages {
		fmt.Fprintf(w, "stage\t%s\n", stage)
	}
	for _, cmd := range r.Commands {
		fmt.Fprintf(w, "command\t%s\t%s %s\n", cmd.Name, cmd.Bin, strings.Join(cmd.Args, " "))
	}
	return nil
}

func Run(ctx context.Context, loaded *config.Loaded, tools Tools, runner toolrunner.Runner, opts Options) Result {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.WaitForTalos == nil {
		opts.WaitForTalos = toolrunner.WaitTCP
	}
	reportStatus(opts.Status, openStateStep, StatusRunning, opts.Now, nil)
	layout, err := state.Open(loaded.Config.Cluster.Name)
	if err != nil {
		reportStatus(opts.Status, openStateStep, StatusFailed, opts.Now, failureFor("state.open", "Could not open bootstrap state", err.Error(), nil))
		return failed(loaded, nil, "NeedsConfig", err.Error())
	}
	reportStatus(opts.Status, openStateStep, StatusDone, opts.Now, nil)
	result := Result{
		Outcome:       "Planned",
		ClusterName:   loaded.Config.Cluster.Name,
		ConfigDigest:  loaded.Digest,
		StateDir:      layout.Root,
		Kubeconfig:    layout.Kubeconfig,
		GenesisBundle: layout.GenesisArchive,
		Stages: []string{
			"OpenState",
			"RenderTalmProject",
			"TalmTemplate",
			"TalmDryRun",
			"TalmApply",
			"TalmBootstrap",
			"TalmKubeconfig",
			"WriteGenesisBundle",
			"RemoveControlPlaneTaint",
			"InstallCozystackOperator",
			"WaitCozystackOperator",
			"ApplyCozystackPlatform",
			"WaitCozystackPlatform",
			"ApplyHelloWorld",
		},
	}
	commands := planCommands(loaded.Config, layout, tools)
	result.Commands = commands
	if !opts.Execute {
		result.Reason = "rerun with --execute to mutate the Talos maintenance target"
		return result
	}
	reportStatus(opts.Status, safetyStep, StatusRunning, opts.Now, nil)
	if !loaded.Config.Bootstrap.Destructive || !loaded.Config.Bootstrap.RequireMaintenance {
		reason := "bootstrap.destructive and bootstrap.requireMaintenance must both be true before reimage"
		reportStatus(opts.Status, safetyStep, StatusBlocked, opts.Now, failureFor("bootstrap.safety", "Bootstrap safety gate is closed", reason, nil))
		result.Outcome = "Refused"
		result.Reason = reason
		return result
	}
	if err := genesis.ValidateRecipients(loaded.Config.Bootstrap.Genesis.AgeRecipients); err != nil {
		reportStatus(opts.Status, safetyStep, StatusBlocked, opts.Now, failureFor("bootstrap.genesis.ageRecipients", "Genesis recipient is missing", err.Error(), nil))
		result.Outcome = "Refused"
		result.Reason = err.Error()
		return result
	}
	reportStatus(opts.Status, safetyStep, StatusDone, opts.Now, nil)
	reportStatus(opts.Status, renderStep, StatusRunning, opts.Now, nil)
	if err := writeGeneratedManifests(loaded.Config, layout); err != nil {
		reportStatus(opts.Status, renderStep, StatusFailed, opts.Now, failureFor("render.manifests", "Could not render bootstrap manifests", err.Error(), nil))
		return failed(loaded, layout, "Retryable", err.Error())
	}
	reportStatus(opts.Status, renderStep, StatusDone, opts.Now, nil)
	for _, cmd := range commands {
		spec := commandStep(cmd.Name)
		reportStatus(opts.Status, spec, StatusRunning, opts.Now, nil)
		switch cmd.Name {
		case "talm-template":
			out, err := runner.Output(ctx, cmd)
			if err != nil {
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failureForCommand(cmd, spec, err))
				return failed(loaded, layout, "Retryable", err.Error())
			}
			if err := state.WriteFile(layout.NodeConfig, out); err != nil {
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failureFor("talos.config.write", "Could not write Talos machine config", err.Error(), &cmd))
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "wait-talos-api":
			if err := opts.WaitForTalos(ctx, loaded.Config.Node.Address, 90*time.Second); err != nil {
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failureFor("talos.api.wait", "Talos API did not become ready", err.Error(), &cmd))
				return failed(loaded, layout, "Retryable", err.Error())
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
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failureFor("genesis.bundle.write", "Could not write genesis bundle", err.Error(), &cmd))
				return failed(loaded, layout, "Retryable", err.Error())
			}
		default:
			if err := runner.Run(ctx, cmd); err != nil {
				reportStatus(opts.Status, spec, StatusFailed, opts.Now, failureForCommand(cmd, spec, err))
				return failed(loaded, layout, "Retryable", err.Error())
			}
		}
		reportStatus(opts.Status, spec, StatusDone, opts.Now, nil)
		if err := state.WriteOperation(layout.Operation, state.Operation{
			ClusterName:  loaded.Config.Cluster.Name,
			ConfigDigest: loaded.Digest,
			Stage:        cmd.Name,
			UpdatedAt:    opts.Now(),
		}); err != nil {
			reportStatus(opts.Status, spec, StatusFailed, opts.Now, failureFor("operation.write", "Could not write operation state", err.Error(), &cmd))
			return failed(loaded, layout, "Retryable", err.Error())
		}
	}
	result.Outcome = "Converged"
	result.Commands = nil
	return result
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
				"--offline",
				"-i",
			},
		},
		{
			Name: "talm-dry-run",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{
				"apply",
				"-f", nodeRel,
				"--talos-version", cfg.Talm.TalosVersion,
				"--kubernetes-version", cfg.Talm.KubernetesVersion,
				"-i",
				"--dry-run",
			},
		},
		{
			Name: "talm-apply",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{
				"apply",
				"-f", nodeRel,
				"--talos-version", cfg.Talm.TalosVersion,
				"--kubernetes-version", cfg.Talm.KubernetesVersion,
				"-i",
			},
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
			Name: "kubectl-apply-platform",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "apply", "-f", layout.Platform},
		},
		toolrunner.Command{
			Name: "kubectl-wait-platform-package",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "wait", "package/cozystack.cozystack-platform", "--for=condition=Ready", "--timeout=10m"},
		},
		toolrunner.Command{
			Name: "kubectl-get-helmreleases",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "get", "hr", "-A"},
		},
	)
	if cfg.Hello.Enabled {
		commands = append(commands, toolrunner.Command{
			Name: "kubectl-apply-hello-world",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "apply", "-f", layout.HelloWorld},
		})
	}
	return commands
}

func genesisFiles(cfg config.Config) []string {
	files := []string{
		"talm/talm.key",
		"talm/secrets.yaml",
		"talm/nodes/controlplane.yaml",
		"talm/kubeconfig",
		"cozystack-platform.yaml",
		"operation.json",
	}
	if cfg.Hello.Enabled {
		files = append(files, "hello-world.yaml")
	}
	return files
}

func writeGeneratedManifests(cfg config.Config, layout *state.Layout) error {
	platform, err := platformManifest(cfg)
	if err != nil {
		return err
	}
	if err := state.WriteFile(layout.Platform, platform); err != nil {
		return fmt.Errorf("write platform manifest: %w", err)
	}
	if cfg.Hello.Enabled {
		hello, err := helloManifest(cfg)
		if err != nil {
			return err
		}
		if err := state.WriteFile(layout.HelloWorld, hello); err != nil {
			return fmt.Errorf("write hello manifest: %w", err)
		}
	}
	return nil
}

func platformManifest(cfg config.Config) ([]byte, error) {
	doc := map[string]any{
		"apiVersion": "cozystack.io/v1alpha1",
		"kind":       "Package",
		"metadata": map[string]any{
			"name": "cozystack.cozystack-platform",
		},
		"spec": map[string]any{
			"variant": cfg.Cozystack.Variant,
			"components": map[string]any{
				"platform": map[string]any{
					"values": map[string]any{
						"publishing": map[string]any{
							"host":              cfg.Cozystack.PublishingHost,
							"apiServerEndpoint": cfg.Cozystack.APIServerEndpoint,
							"exposedServices":   cfg.Cozystack.ExposedServices,
						},
						"networking": map[string]any{
							"podCIDR":     cfg.Cluster.PodCIDR,
							"podGateway":  "10.244.0.1",
							"serviceCIDR": cfg.Cluster.ServiceCIDR,
							"joinCIDR":    cfg.Cluster.JoinCIDR,
						},
					},
				},
			},
		},
	}
	return yaml.Marshal(doc)
}

func helloManifest(cfg config.Config) ([]byte, error) {
	docs := []map[string]any{
		{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]any{
				"name": cfg.Hello.Namespace,
			},
		},
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      "guardian-hello",
				"namespace": cfg.Hello.Namespace,
			},
			"data": map[string]any{
				"message": "hello from Guardian on Cozystack",
			},
		},
	}
	var out []byte
	for i, doc := range docs {
		raw, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			out = append(out, []byte("---\n")...)
		}
		out = append(out, raw...)
	}
	return out, nil
}

func failed(loaded *config.Loaded, layout *state.Layout, outcome, reason string) Result {
	stateDir := ""
	kubeconfig := ""
	if layout != nil {
		stateDir = layout.Root
		kubeconfig = layout.Kubeconfig
		genesisBundle := layout.GenesisArchive
		return Result{
			Outcome:       outcome,
			Reason:        reason,
			ClusterName:   loaded.Config.Cluster.Name,
			ConfigDigest:  loaded.Digest,
			StateDir:      stateDir,
			Kubeconfig:    kubeconfig,
			GenesisBundle: genesisBundle,
		}
	}
	return Result{
		Outcome:      outcome,
		Reason:       reason,
		ClusterName:  loaded.Config.Cluster.Name,
		ConfigDigest: loaded.Digest,
		StateDir:     stateDir,
		Kubeconfig:   kubeconfig,
	}
}
