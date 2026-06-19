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
	"github.com/guardian-intelligence/guardian/src/guardian/internal/latitude"
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

func TestRunExecuteUsesRuntimeGenesisRecipient(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	loaded := testLoaded()
	loaded.Config.Bootstrap.Genesis.AgeRecipients = nil
	runner := &fakeRunner{output: []byte("machine config")}

	result := Run(context.Background(), loaded, testTools(), runner, Options{
		Execute:              true,
		GenesisAgeRecipients: []string{identity.Recipient().String()},
		Now:                  func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
		WaitForTalos: func(context.Context, string, time.Duration) error {
			return nil
		},
	})
	if result.Outcome != "Converged" {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
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
		"talos-maintenance-disks",
		"talos-maintenance-links",
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
	values, err := os.ReadFile(filepath.Join(result.StateDir, "talm", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "206.223.228.100/31") {
		t.Fatalf("values.yaml does not pin advertised subnet:\n%s", values)
	}
	hostPatch, err := os.ReadFile(filepath.Join(result.StateDir, "talm", "guardian-host-patch.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hostPatch), "362510FCEFB8") || !strings.Contains(string(hostPatch), "gi-ash-bm-001") {
		t.Fatalf("host patch missing serial/hostname:\n%s", hostPatch)
	}
	raw, err := os.ReadFile(result.GenesisBundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "age-encryption.org/v1") {
		t.Fatalf("genesis bundle is not age encrypted")
	}
}

func TestRunExecuteRunsLatitudeReinstallBeforeTalm(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	schematic := filepath.Join(root, "talos", "schematic.yaml")
	if err := os.MkdirAll(filepath.Dir(schematic), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schematic, []byte("customization: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := testLoaded()
	loaded.Path = filepath.Join(root, "up.cue")
	loaded.Config.Provider = config.ProviderSpec{
		Name:            "latitude",
		ServerID:        "sv_dev",
		TokenEnv:        "LATITUDE_API_KEY",
		Reinstall:       true,
		TalosSchematic:  "talos/schematic.yaml",
		TalosVersion:    "v1.13.4",
		RefuseProdNames: true,
	}
	recorder := &callRecorder{}
	runner := &fakeRunner{output: []byte("machine config")}
	lat := &fakeLatitude{
		recorder: recorder,
		server: latitude.Server{
			ID:          "sv_dev",
			Hostname:    "vs-dev-w0",
			PrimaryIPv4: "206.223.228.101",
			Project:     "guardian",
		},
	}

	result := Run(context.Background(), loaded, testTools(), runner, Options{
		Execute:  true,
		Latitude: lat,
		RegisterSchematic: func(_ context.Context, path string) (string, error) {
			if path != schematic {
				t.Fatalf("schematic path = %q, want %q", path, schematic)
			}
			recorder.add("register-schematic")
			return "schematic-id", nil
		},
		WaitForTalos: func(_ context.Context, address string, _ time.Duration) error {
			recorder.add("wait-talos:" + address)
			return nil
		},
	})
	if result.Outcome != "Converged" {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
	}
	wantCalls := "register-schematic,get-server:sv_dev,reinstall-ipxe:sv_dev:https://pxe.factory.talos.dev/pxe/schematic-id/v1.13.4/metal-amd64"
	if got := recorder.String(); got != wantCalls {
		t.Fatalf("provider calls = %s, want %s", got, wantCalls)
	}
	if len(runner.commands) < 2 || runner.commands[0].Name != "wait-talos-maintenance" || runner.commands[1].Name != "talm-init" {
		t.Fatalf("runner commands = %#v, want maintenance probe before talm-init", runner.commands)
	}
}

func TestRunExecuteRefusesProdLookingLatitudeServer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	schematic := filepath.Join(root, "talos", "schematic.yaml")
	if err := os.MkdirAll(filepath.Dir(schematic), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schematic, []byte("customization: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := testLoaded()
	loaded.Path = filepath.Join(root, "up.cue")
	loaded.Config.Provider = config.ProviderSpec{
		Name:            "latitude",
		ServerID:        "sv_dev",
		TokenEnv:        "LATITUDE_API_KEY",
		Reinstall:       true,
		TalosSchematic:  "talos/schematic.yaml",
		TalosVersion:    "v1.13.4",
		RefuseProdNames: true,
	}
	lat := &fakeLatitude{
		server: latitude.Server{
			ID:          "sv_dev",
			Hostname:    "prod-host",
			PrimaryIPv4: "206.223.228.101",
			Project:     "guardian",
		},
	}

	result := Run(context.Background(), loaded, testTools(), &fakeRunner{}, Options{
		Execute:  true,
		Latitude: lat,
		RegisterSchematic: func(context.Context, string) (string, error) {
			return "schematic-id", nil
		},
	})
	if result.Outcome != "Refused" {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
	if !strings.Contains(result.Reason, "server.hostname looks production") {
		t.Fatalf("reason = %q, want prod hostname refusal", result.Reason)
	}
}

type fakeRunner struct {
	commands []toolrunner.Command
	output   []byte
	outputs  map[string][]byte
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
		if err := os.WriteFile(filepath.Join(cmd.Dir, "values.yaml"), []byte("endpoint: \"\"\nadvertisedSubnets: []\ncertSANs: []\n"), 0o600); err != nil {
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
	if r.outputs != nil {
		if out, ok := r.outputs[cmd.Name]; ok {
			return out, nil
		}
	}
	switch cmd.Name {
	case "wait-talos-maintenance":
		return []byte("SERIAL 362510FCEFB8\n"), nil
	case "talos-maintenance-disks":
		return []byte("SERIAL 362510FCEFB8\n"), nil
	case "talos-maintenance-links":
		return []byte("HARDWAREADDR 90:5a:08:33:ba:9f\n"), nil
	}
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

type fakeLatitude struct {
	recorder *callRecorder
	server   latitude.Server
}

func (f *fakeLatitude) GetServer(_ context.Context, serverID string) (latitude.Server, error) {
	if f.recorder != nil {
		f.recorder.add("get-server:" + serverID)
	}
	return f.server, nil
}

func (f *fakeLatitude) ReinstallIPXE(_ context.Context, serverID, _, ipxeURL string) error {
	if f.recorder != nil {
		f.recorder.add("reinstall-ipxe:" + serverID + ":" + ipxeURL)
	}
	return nil
}

type callRecorder struct {
	calls []string
}

func (r *callRecorder) add(call string) {
	r.calls = append(r.calls, call)
}

func (r *callRecorder) String() string {
	return strings.Join(r.calls, ",")
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
