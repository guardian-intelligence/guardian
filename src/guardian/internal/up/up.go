package up

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/genesis"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/latitude"
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
	Execute              bool
	GenesisAgeRecipients []string
	Latitude             LatitudeClient
	Now                  func() time.Time
	RegisterSchematic    func(context.Context, string) (string, error)
	WaitForTalos         func(context.Context, string, time.Duration) error
}

type LatitudeClient interface {
	GetServer(context.Context, string) (latitude.Server, error)
	ReinstallIPXE(context.Context, string, string, string) error
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
	if opts.RegisterSchematic == nil {
		opts.RegisterSchematic = registerSchematic
	}
	layout, err := state.Open(loaded.Config.Cluster.Name)
	if err != nil {
		return failed(loaded, nil, "NeedsConfig", err.Error())
	}
	stages := []string{"OpenState"}
	if loaded.Config.Provider.Reinstall {
		stages = append(stages,
			"RegisterTalosSchematic",
			"LatitudeReinstall",
			"WaitTalosMaintenance",
		)
	}
	stages = append(stages,
		"RenderTalmProject",
		"WriteTalmValues",
		"WriteGuardianHostPatch",
		"ValidateMaintenanceInventory",
		"TalmTemplate",
		"TalmDryRun",
		"TalmApply",
		"TalmBootstrap",
		"TalmKubeconfig",
		"WaitKubernetesAPI",
		"WriteGenesisBundle",
		"RemoveControlPlaneTaint",
		"InstallCozystackOperator",
		"WaitCozystackOperator",
		"ApplyCozystackPlatform",
		"WaitCozystackPlatform",
		"WaitNodeReady",
		"ApplyHelloWorld",
	)
	result := Result{
		Outcome:       "Planned",
		ClusterName:   loaded.Config.Cluster.Name,
		ConfigDigest:  loaded.Digest,
		StateDir:      layout.Root,
		Kubeconfig:    layout.Kubeconfig,
		GenesisBundle: layout.GenesisArchive,
		Stages:        stages,
	}
	commands := planCommands(loaded.Config, layout, tools)
	result.Commands = commands
	if !opts.Execute {
		result.Reason = "rerun with --execute to mutate the Talos maintenance target"
		return result
	}
	if !loaded.Config.Bootstrap.Destructive || !loaded.Config.Bootstrap.RequireMaintenance {
		result.Outcome = "Refused"
		result.Reason = "bootstrap.destructive and bootstrap.requireMaintenance must both be true before reimage"
		return result
	}
	recipients := effectiveRecipients(loaded.Config, opts)
	if err := genesis.ValidateRecipients(recipients); err != nil {
		result.Outcome = "Refused"
		result.Reason = err.Error()
		return result
	}
	if err := writeGeneratedManifests(loaded.Config, layout); err != nil {
		return failed(loaded, layout, "Retryable", err.Error())
	}
	var schematicID string
	for _, cmd := range commands {
		switch cmd.Name {
		case "talos-factory-schematic":
			path, err := providerSchematicPath(loaded)
			if err != nil {
				return failed(loaded, layout, "NeedsConfig", err.Error())
			}
			schematicID, err = opts.RegisterSchematic(ctx, path)
			if err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "latitude-reinstall-ipxe":
			if schematicID == "" {
				path, err := providerSchematicPath(loaded)
				if err != nil {
					return failed(loaded, layout, "NeedsConfig", err.Error())
				}
				schematicID, err = opts.RegisterSchematic(ctx, path)
				if err != nil {
					return failed(loaded, layout, "Retryable", err.Error())
				}
			}
			if err := reinstallLatitude(ctx, loaded.Config, opts.Latitude, talosPXEURL(schematicID, loaded.Config.Provider.TalosVersion)); err != nil {
				return failed(loaded, layout, "Refused", err.Error())
			}
		case "wait-talos-maintenance":
			out, err := waitCommandOutput(ctx, runner, cmd, 15*time.Minute, 5*time.Second)
			if err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
			if err := requireInventoryValue("install disk serial", loaded.Config.Node.InstallDiskSerial, out); err != nil {
				return failed(loaded, layout, "Refused", err.Error())
			}
		case "write-talm-values":
			if err := writeTalmValues(loaded.Config, layout); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "write-guardian-host-patch":
			if err := writeHostPatch(loaded.Config, layout); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "talos-maintenance-disks":
			out, err := runner.Output(ctx, cmd)
			if err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
			if err := requireInventoryValue("install disk serial", loaded.Config.Node.InstallDiskSerial, out); err != nil {
				return failed(loaded, layout, "Refused", err.Error())
			}
		case "talos-maintenance-links":
			out, err := runner.Output(ctx, cmd)
			if err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
			if err := requireInventoryValue("interface MAC", loaded.Config.Node.InterfaceMAC, out); err != nil {
				return failed(loaded, layout, "Refused", err.Error())
			}
		case "talm-template":
			out, err := runner.Output(ctx, cmd)
			if err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
			if err := state.WriteFile(layout.NodeConfig, out); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "talm-init":
			initialized, err := talmSecretStateInitialized(layout)
			if err != nil {
				return failed(loaded, layout, "Refused", err.Error())
			}
			if initialized {
				break
			}
			if err := runner.Run(ctx, cmd); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "wait-talos-api":
			if _, err := waitCommandOutput(ctx, runner, cmd, 10*time.Minute, 5*time.Second); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "kubectl-wait-kubernetes-api":
			if _, err := waitCommandOutput(ctx, runner, cmd, 10*time.Minute, 5*time.Second); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "kubectl-wait-node-ready":
			if _, err := waitCommandOutput(ctx, runner, cmd, 10*time.Minute, 5*time.Second); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "kubectl-remove-control-plane-taint":
			if _, err := runner.Output(ctx, cmd); err != nil && !missingControlPlaneTaint(err) {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		case "write-genesis-bundle":
			if _, err := genesis.WriteEncrypted(genesis.Bundle{
				OutputPath:   layout.GenesisArchive,
				Root:         layout.Root,
				ClusterName:  loaded.Config.Cluster.Name,
				ConfigDigest: loaded.Digest,
				CreatedAt:    opts.Now(),
				Recipients:   recipients,
				Files:        genesisFiles(loaded.Config),
			}); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		default:
			if cmd.Name == "talm-bootstrap" {
				if err := waitCommandRun(ctx, runner, cmd, 10*time.Minute, 10*time.Second); err != nil {
					return failed(loaded, layout, "Retryable", err.Error())
				}
				break
			}
			if err := runner.Run(ctx, cmd); err != nil {
				return failed(loaded, layout, "Retryable", err.Error())
			}
		}
		if err := state.WriteOperation(layout.Operation, state.Operation{
			ClusterName:  loaded.Config.Cluster.Name,
			ConfigDigest: loaded.Digest,
			Stage:        cmd.Name,
			UpdatedAt:    opts.Now(),
		}); err != nil {
			return failed(loaded, layout, "Retryable", err.Error())
		}
	}
	result.Outcome = "Converged"
	result.Commands = nil
	return result
}

func planCommands(cfg config.Config, layout *state.Layout, tools Tools) []toolrunner.Command {
	nodeRel := filepath.ToSlash(strings.TrimPrefix(layout.NodeConfig, layout.TalmProject+string(os.PathSeparator)))
	hostPatchRel := filepath.ToSlash(strings.TrimPrefix(layout.HostPatch, layout.TalmProject+string(os.PathSeparator)))
	var commands []toolrunner.Command
	if cfg.Provider.Reinstall {
		commands = append(commands,
			toolrunner.Command{
				Name: "talos-factory-schematic",
				Bin:  "guardian-internal",
				Args: []string{"schematic", cfg.Provider.TalosSchematic, cfg.Provider.TalosVersion},
			},
			toolrunner.Command{
				Name: "latitude-reinstall-ipxe",
				Bin:  "guardian-internal",
				Args: []string{"latitude-reinstall-ipxe", cfg.Provider.ServerID, cfg.Node.Hostname},
			},
			toolrunner.Command{
				Name: "wait-talos-maintenance",
				Bin:  tools.Talos,
				Args: []string{"get", "disks", "--insecure", "-n", cfg.Node.Address},
			},
		)
	}
	commands = append(commands,
		toolrunner.Command{
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
			},
		},
		toolrunner.Command{
			Name: "write-talm-values",
			Bin:  "guardian-internal",
			Args: []string{"talm-values", layout.TalmValues},
		},
		toolrunner.Command{
			Name: "write-guardian-host-patch",
			Bin:  "guardian-internal",
			Args: []string{"host-patch", layout.HostPatch},
		},
		toolrunner.Command{
			Name: "talos-maintenance-disks",
			Bin:  tools.Talos,
			Args: []string{"get", "disks", "--insecure", "-n", cfg.Node.Address},
		},
		toolrunner.Command{
			Name: "talos-maintenance-links",
			Bin:  tools.Talos,
			Args: []string{"get", "links", "--insecure", "-n", cfg.Node.Address},
		},
		toolrunner.Command{
			Name:   "talm-template",
			Bin:    tools.Talm,
			Dir:    layout.TalmProject,
			Secret: true,
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
		toolrunner.Command{
			Name:   "talm-dry-run",
			Bin:    tools.Talm,
			Dir:    layout.TalmProject,
			Secret: true,
			Args: []string{
				"apply",
				"-f", nodeRel,
				"-f", hostPatchRel,
				"--talos-version", cfg.Talm.TalosVersion,
				"--kubernetes-version", cfg.Talm.KubernetesVersion,
				"-i",
				"--dry-run",
			},
		},
		toolrunner.Command{
			Name:   "talm-apply",
			Bin:    tools.Talm,
			Dir:    layout.TalmProject,
			Secret: true,
			Args: []string{
				"apply",
				"-f", nodeRel,
				"-f", hostPatchRel,
				"--talos-version", cfg.Talm.TalosVersion,
				"--kubernetes-version", cfg.Talm.KubernetesVersion,
				"-i",
				"--mode", "reboot",
			},
		},
		toolrunner.Command{
			Name: "wait-talos-api",
			Bin:  tools.Talos,
			Args: []string{"--talosconfig", filepath.Join(layout.TalmProject, "talosconfig"), "-n", cfg.Node.Address, "-e", cfg.Node.Address, "version", "--short"},
		},
		toolrunner.Command{
			Name: "talm-bootstrap",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{"bootstrap", "-f", nodeRel},
		},
		toolrunner.Command{
			Name: "talm-kubeconfig",
			Bin:  tools.Talm,
			Dir:  layout.TalmProject,
			Args: []string{"kubeconfig", "-f", nodeRel},
		},
		toolrunner.Command{
			Name: "kubectl-wait-kubernetes-api",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "get", "--raw=/readyz"},
		},
		toolrunner.Command{
			Name: "write-genesis-bundle",
			Bin:  "guardian-internal",
			Args: []string{"genesis", layout.GenesisArchive},
		},
	)
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
		toolrunner.Command{
			Name: "kubectl-wait-node-ready",
			Bin:  tools.Kubectl,
			Args: []string{"--kubeconfig", layout.Kubeconfig, "wait", "nodes", "--all", "--for=condition=Ready", "--timeout=10m"},
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

func effectiveRecipients(cfg config.Config, opts Options) []string {
	if len(opts.GenesisAgeRecipients) > 0 {
		return opts.GenesisAgeRecipients
	}
	return cfg.Bootstrap.Genesis.AgeRecipients
}

func providerSchematicPath(loaded *config.Loaded) (string, error) {
	raw := strings.TrimSpace(loaded.Config.Provider.TalosSchematic)
	if raw == "" {
		return "", fmt.Errorf("provider.talosSchematic is required for provider reinstall")
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	base := filepath.Dir(loaded.Path)
	if loaded.Path == "" {
		base = "."
	}
	return filepath.Clean(filepath.Join(base, raw)), nil
}

func talosPXEURL(schematicID, talosVersion string) string {
	return fmt.Sprintf("https://pxe.factory.talos.dev/pxe/%s/%s/metal-amd64", schematicID, talosVersion)
}

func reinstallLatitude(ctx context.Context, cfg config.Config, client LatitudeClient, ipxeURL string) error {
	if cfg.Provider.Name != "latitude" || cfg.Provider.ServerID == "" {
		return fmt.Errorf("provider.latitude serverId is required before reinstall")
	}
	if client == nil {
		token := strings.TrimSpace(os.Getenv(cfg.Provider.TokenEnv))
		if token == "" {
			return fmt.Errorf("%s must contain a Latitude API token before provider reinstall", cfg.Provider.TokenEnv)
		}
		client = latitude.Client{BaseURL: latitude.DefaultBaseURL, Token: token}
	}
	server, err := client.GetServer(ctx, cfg.Provider.ServerID)
	if err != nil {
		return err
	}
	if server.ID != "" && server.ID != cfg.Provider.ServerID {
		return fmt.Errorf("Latitude server id mismatch: API returned %s for %s", server.ID, cfg.Provider.ServerID)
	}
	if server.PrimaryIPv4 != cfg.Node.Address {
		return fmt.Errorf("Latitude server %s has primary IPv4 %s, want %s", cfg.Provider.ServerID, server.PrimaryIPv4, cfg.Node.Address)
	}
	if server.Locked {
		return fmt.Errorf("Latitude server %s is locked", cfg.Provider.ServerID)
	}
	if cfg.Provider.RefuseProdNames {
		for label, value := range map[string]string{
			"cluster.name":      cfg.Cluster.Name,
			"node.name":         cfg.Node.Name,
			"node.hostname":     cfg.Node.Hostname,
			"server.hostname":   server.Hostname,
			"server.project":    server.Project,
			"provider.serverId": cfg.Provider.ServerID,
		} {
			if looksProd(value) {
				return fmt.Errorf("refusing provider reinstall because %s looks production: %s", label, value)
			}
		}
	}
	if err := client.ReinstallIPXE(ctx, cfg.Provider.ServerID, cfg.Node.Hostname, ipxeURL); err != nil {
		if errors.Is(err, latitude.ErrServerBeingProvisioned) {
			return nil
		}
		return err
	}
	return nil
}

func looksProd(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "prod") || strings.Contains(value, "production")
}

type factoryResponse struct {
	ID string `json:"id"`
}

func registerSchematic(ctx context.Context, path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Talos schematic %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://factory.talos.dev/schematics", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-yaml")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("register Talos schematic: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read Talos factory response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Talos factory returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var decoded factoryResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("decode Talos factory response: %w", err)
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("Talos factory response missing schematic id")
	}
	return decoded.ID, nil
}

func genesisFiles(cfg config.Config) []string {
	files := []string{
		"talm/talm.key",
		"talm/secrets.yaml",
		"talm/values.yaml",
		"talm/nodes/controlplane.yaml",
		"talm/guardian-host-patch.yaml",
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

func writeTalmValues(cfg config.Config, layout *state.Layout) error {
	raw, err := os.ReadFile(layout.TalmValues)
	if err != nil {
		return fmt.Errorf("read Talm values: %w", err)
	}
	values := map[string]any{}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("decode Talm values: %w", err)
	}
	values["endpoint"] = cfg.Cluster.Endpoint
	values["clusterName"] = cfg.Cluster.Name
	values["image"] = cfg.Talm.InstallerImage
	values["podSubnets"] = []string{cfg.Cluster.PodCIDR}
	values["serviceSubnets"] = []string{cfg.Cluster.ServiceCIDR}
	values["advertisedSubnets"] = []string{cfg.Cluster.AdvertisedCIDR}
	if certSANs := talmCertSANs(cfg); len(certSANs) > 0 {
		values["certSANs"] = certSANs
	}
	out, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode Talm values: %w", err)
	}
	return state.WriteFile(layout.TalmValues, out)
}

func talmCertSANs(cfg config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || value == "127.0.0.1" {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	add(cfg.Node.Address)
	if u, err := url.Parse(cfg.Cluster.Endpoint); err == nil {
		add(u.Hostname())
	}
	add(cfg.Cluster.APIServerDomain)
	return out
}

func writeHostPatch(cfg config.Config, layout *state.Layout) error {
	docs := []map[string]any{
		{
			"machine": map[string]any{
				"install": map[string]any{
					"diskSelector": map[string]any{
						"serial": cfg.Node.InstallDiskSerial,
					},
				},
			},
		},
		{
			"apiVersion": "v1alpha1",
			"kind":       "HostnameConfig",
			"hostname":   cfg.Node.Hostname,
		},
	}
	var out []byte
	for i, doc := range docs {
		raw, err := yaml.Marshal(doc)
		if err != nil {
			return fmt.Errorf("encode host patch: %w", err)
		}
		if i > 0 {
			out = append(out, []byte("---\n")...)
		}
		out = append(out, raw...)
	}
	return state.WriteFile(layout.HostPatch, out)
}

func requireInventoryValue(label, value string, inventory []byte) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required before destructive bootstrap", label)
	}
	if !strings.Contains(strings.ToLower(string(inventory)), strings.ToLower(value)) {
		return fmt.Errorf("%s %s not found in Talos maintenance inventory", label, value)
	}
	return nil
}

func waitCommandOutput(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout, interval time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := runner.Output(ctx, cmd)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for %s: %w", timeout, cmd.Name, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func waitCommandRun(ctx context.Context, runner toolrunner.Runner, cmd toolrunner.Command, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		err := runner.Run(ctx, cmd)
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s: %w", timeout, cmd.Name, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func talmSecretStateInitialized(layout *state.Layout) (bool, error) {
	keyExists, err := fileExists(filepath.Join(layout.TalmProject, "talm.key"))
	if err != nil {
		return false, err
	}
	secretsExist, err := fileExists(filepath.Join(layout.TalmProject, "secrets.yaml"))
	if err != nil {
		return false, err
	}
	if keyExists && secretsExist {
		for _, path := range []string{
			layout.TalmValues,
			filepath.Join(layout.TalmProject, "templates", "controlplane.yaml"),
		} {
			exists, err := fileExists(path)
			if err != nil {
				return false, err
			}
			if !exists {
				return false, fmt.Errorf("incomplete Talm bootstrap state in %s: missing %s; refusing to regenerate trust material", layout.TalmProject, path)
			}
		}
		return true, nil
	}
	if keyExists || secretsExist {
		return false, fmt.Errorf("partial Talm bootstrap secret state in %s; refusing to regenerate trust material", layout.TalmProject)
	}
	return false, nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func missingControlPlaneTaint(err error) bool {
	text := err.Error()
	return strings.Contains(text, "not found") && strings.Contains(text, "node-role.kubernetes.io/control-plane")
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
