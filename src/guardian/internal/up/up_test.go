package up

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/toolrunner"
)

func TestRunPlansWithoutExecuting(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	loaded := testLoaded()
	runner := &fakeRunner{}

	result := Run(context.Background(), loaded, testTools(), runner, Options{})
	if result.Outcome != "Planned" {
		t.Fatalf("outcome = %s, want Planned: %#v", result.Outcome, result)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("runner commands = %v, want none", runner.commands)
	}
	if !strings.Contains(result.Reason, "--execute") {
		t.Fatalf("reason = %q, want execute hint", result.Reason)
	}
	if len(result.Commands) == 0 || result.Commands[0].Name != "talm-init" {
		t.Fatalf("commands = %#v, want talm-init first", result.Commands)
	}
}

func TestRunExecuteRefusesWithoutDestructiveMaintenanceGate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	loaded := testLoaded()
	loaded.Config.Bootstrap.RequireMaintenance = false

	result := Run(context.Background(), loaded, testTools(), &fakeRunner{}, Options{Execute: true})
	if result.Outcome != "Refused" {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
}

func TestRunExecuteRefusesWithoutGenesisRecipient(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	loaded := testLoaded()
	loaded.Config.Bootstrap.Genesis.AgeRecipients = nil

	result := Run(context.Background(), loaded, testTools(), &fakeRunner{}, Options{Execute: true})
	if result.Outcome != "Refused" {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
	if !strings.Contains(result.Reason, "ageRecipients") {
		t.Fatalf("reason = %q, want ageRecipients", result.Reason)
	}
}

func TestRunExecuteUsesPinnedToolCommands(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runner := &fakeRunner{output: []byte("machine config")}

	result := Run(context.Background(), testLoaded(), testTools(), runner, Options{
		Execute: true,
		Now:     func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
		WaitForTalos: func(context.Context, string, time.Duration) error {
			return nil
		},
	})
	if result.Outcome != "Converged" {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
	}
	got := runner.names()
	for _, want := range []string{
		"talm-init",
		"talm-template",
		"talm-dry-run",
		"talm-apply",
		"talm-bootstrap",
		"talm-kubeconfig",
		"helm-install-cozystack",
		"kubectl-apply-platform",
		"kubectl-apply-hello-world",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("commands = %s, missing %s", got, want)
		}
	}
	for _, cmd := range runner.commands {
		if strings.Contains(cmd.Bin, "/usr/bin") || strings.Contains(cmd.Bin, "PATH") {
			t.Fatalf("command uses ambient tool path: %#v", cmd)
		}
	}
	if !strings.Contains(strings.Join(flattenArgs(runner.commands), " "), "--kubernetes-version 1.36.1") {
		t.Fatalf("commands do not include Kubernetes version pin: %#v", runner.commands)
	}
	raw, err := os.ReadFile(result.GenesisBundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "age-encryption.org/v1") {
		t.Fatalf("genesis bundle is not age encrypted")
	}
}

type fakeRunner struct {
	commands []toolrunner.Command
	output   []byte
}

func (r *fakeRunner) Run(_ context.Context, cmd toolrunner.Command) error {
	r.commands = append(r.commands, cmd)
	switch cmd.Name {
	case "talm-init":
		if err := os.WriteFile(filepath.Join(cmd.Dir, "talm.key"), []byte("talm key"), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cmd.Dir, "secrets.yaml"), []byte("secrets"), 0o600); err != nil {
			return err
		}
	case "talm-kubeconfig":
		if err := os.WriteFile(filepath.Join(cmd.Dir, "kubeconfig"), []byte("kubeconfig"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (r *fakeRunner) Output(_ context.Context, cmd toolrunner.Command) ([]byte, error) {
	r.commands = append(r.commands, cmd)
	return r.output, nil
}

func (r *fakeRunner) names() string {
	var names []string
	for _, cmd := range r.commands {
		names = append(names, cmd.Name)
	}
	return strings.Join(names, ",")
}

func flattenArgs(commands []toolrunner.Command) []string {
	var out []string
	for _, cmd := range commands {
		out = append(out, cmd.Args...)
	}
	return out
}

func testLoaded() *config.Loaded {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	cfg := config.Config{
		Cluster: config.ClusterSpec{
			Name:           "guardian-dev",
			Endpoint:       "https://206.223.228.101:6443",
			Domain:         "guardianintelligence.org",
			PodCIDR:        "10.244.0.0/16",
			ServiceCIDR:    "10.96.0.0/16",
			JoinCIDR:       "100.64.0.0/16",
			AdvertisedCIDR: "206.223.228.100/31",
		},
		Node: config.NodeSpec{
			Name:              "ash-bm-001",
			Address:           "206.223.228.101",
			Hostname:          "gi-ash-bm-001",
			InterfaceMAC:      "90:5a:08:33:ba:9f",
			InstallDiskSerial: "362510FCEFB8",
		},
		Talm: config.TalmSpec{
			Preset:            "cozystack",
			TalosVersion:      "v1.13",
			KubernetesVersion: "1.36.1",
			InstallerImage:    "ghcr.io/cozystack/cozystack/talos:v1.13.0",
			Template:          "templates/controlplane.yaml",
		},
		Cozystack: config.CozystackSpec{
			Version:            "1.4.1",
			Variant:            "isp-full",
			PublishingHost:     "dev.guardianintelligence.org",
			APIServerEndpoint:  "https://api.dev.guardianintelligence.org:443",
			ExposedServices:    []string{"dashboard", "api"},
			RemoveControlTaint: true,
		},
		Bootstrap: config.BootstrapSpec{
			Destructive:        true,
			RequireMaintenance: true,
			TargetState:        "talos-maintenance",
			Genesis: config.GenesisSpec{
				AgeRecipients: []string{identity.Recipient().String()},
			},
		},
		Hello: config.HelloSpec{Enabled: true, Namespace: "guardian-hello"},
	}
	return &config.Loaded{Config: cfg, Digest: "digest"}
}

func testTools() Tools {
	return Tools{
		Talm:    "/runfiles/talm",
		Talos:   "/runfiles/talosctl",
		Kubectl: "/runfiles/kubectl",
		Helm:    "/runfiles/helm",
	}
}
