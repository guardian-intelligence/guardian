package up

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/state"
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
	if result.Code != "" {
		t.Fatalf("code = %q, want empty planned result code", result.Code)
	}
	if len(result.Commands) == 0 || result.Commands[0].Name != "talm-init" {
		t.Fatalf("commands = %#v, want talm-init first", result.Commands)
	}
	if !strings.Contains(commandNames(result.Commands), "boot-to-talos-install") {
		t.Fatalf("planned commands = %#v, want boot-to-talos-install", result.Commands)
	}
	if !strings.Contains(commandNames(result.Commands), "write-talm-values") {
		t.Fatalf("planned commands = %#v, want write-talm-values", result.Commands)
	}
	if !strings.Contains(commandNames(result.Commands), "write-talm-template-overrides") {
		t.Fatalf("planned commands = %#v, want write-talm-template-overrides", result.Commands)
	}
	if !strings.Contains(commandNames(result.Commands), "wait-talos-maintenance-api") {
		t.Fatalf("planned commands = %#v, want wait-talos-maintenance-api", result.Commands)
	}
}

func TestResultTextOmitsCommandGraph(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	result := Run(context.Background(), testLoaded(), testTools(), &fakeRunner{}, Options{})

	var buf bytes.Buffer
	if err := result.Text(&buf); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, forbidden := range []string{"reason\t", "detail\t", "stage\t", "command\t", "/runfiles/talm", "talm-init", "\nnext\t", "\ndetails\t"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("text output leaked %q:\n%s", forbidden, text)
		}
	}
	for _, want := range []string{
		"outcome\tPlanned",
		"source\tsrc/hosts/ash-bm-004/host.cue",
		"target\t206.223.228.87",
		"will\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestResultTextOmitsUnsetFields(t *testing.T) {
	result := Result{
		Outcome:    "NeedsConfig",
		Code:       "config.load",
		SourcePath: "src/hosts/ash-bm-001/host.cue",
	}

	var buf bytes.Buffer
	if err := result.Text(&buf); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, forbidden := range []string{"cluster\t", "state\t", "target\t", "kubeconfig\t", "genesisBundle\t"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("text output leaked unset field %q:\n%s", forbidden, text)
		}
	}
	for _, want := range []string{
		"outcome\tNeedsConfig",
		"code\tconfig.load",
		"source\tsrc/hosts/ash-bm-001/host.cue",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q:\n%s", want, text)
		}
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
	if result.Code != "bootstrap.safety" {
		t.Fatalf("code = %q, want bootstrap.safety", result.Code)
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
	if result.Code != "bootstrap.genesis.ageRecipients" {
		t.Fatalf("code = %q, want bootstrap.genesis.ageRecipients", result.Code)
	}
}

func TestCommandFailureUsesCodeOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runner := &fakeRunner{
		output:    []byte("failed to render templates: advertisedSubnets was left empty"),
		outputErr: errors.New("/runfiles/talm template: exit status 1"),
	}

	result := Run(context.Background(), testLoaded(), testTools(), runner, Options{Execute: true})
	if result.Outcome != "Retryable" {
		t.Fatalf("outcome = %s, want Retryable: %#v", result.Outcome, result)
	}
	if result.Code != "talm-template" {
		t.Fatalf("code = %q, want talm-template", result.Code)
	}
}

func TestRunExecuteUsesPinnedToolCommands(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runner := &fakeRunner{output: []byte("machine config")}

	result := Run(context.Background(), testLoaded(), testTools(), runner, Options{
		Execute: true,
		Now:     func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
		WaitForTalos: func(_ context.Context, _ string, timeout time.Duration) error {
			if timeout == 2*time.Second {
				return errors.New("not ready")
			}
			return nil
		},
		RunBootToTalos: func(_ context.Context, cfg config.Config, bootToTalos string) error {
			runner.commands = append(runner.commands, toolrunner.Command{
				Name: "boot-to-talos-install",
				Bin:  bootToTalos,
				Args: bootToTalosArgs(cfg),
			})
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
		"boot-to-talos-install",
		"talm-dry-run",
		"talm-apply",
		"talm-bootstrap",
		"talm-kubeconfig",
		"kubectl-wait-node-registered",
		"helm-install-cozystack",
		"kubectl-apply-cozystack-platform",
		"kubectl-wait-platform-package",
		"kubectl-wait-node-ready",
		"kubectl-wait-cozystack-helmreleases",
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
	if !strings.Contains(strings.Join(flattenArgs(runner.commands), " "), "--offline") {
		t.Fatalf("commands do not render Talos config offline: %#v", runner.commands)
	}
	if !strings.Contains(strings.Join(flattenArgs(runner.commands), " "), "-mode boot -image ghcr.io/cozystack/cozystack/talos:v1.13.0 -yes") {
		t.Fatalf("commands do not kexec Talos using boot-to-talos boot mode: %#v", runner.commands)
	}
	if !strings.Contains(strings.Join(flattenArgs(runner.commands), " "), "--mode reboot") {
		t.Fatalf("commands do not reboot after applying Talos config: %#v", runner.commands)
	}
	valuesRaw, err := os.ReadFile(filepath.Join(result.StateDir, "talm", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	valuesText := string(valuesRaw)
	if !strings.Contains(valuesText, "advertisedSubnets: [206.223.228.86/31]") &&
		!strings.Contains(valuesText, "advertisedSubnets:\n  - 206.223.228.86/31") {
		t.Fatalf("talm values do not pin advertisedSubnets:\n%s", valuesRaw)
	}
	helperRaw, err := os.ReadFile(filepath.Join(result.StateDir, "talm", "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	helperText := string(helperRaw)
	for _, want := range []string{
		`define "talos.config.network.multidoc"`,
		"kind: HostnameConfig",
		`hostname: "gi-ash-bm-004"`,
		"kind: ResolverConfig",
		`address: "1.1.1.1"`,
		`address: "8.8.8.8"`,
		"diskSelector:",
		`serial: "362510FE3218"`,
	} {
		if !strings.Contains(helperText, want) {
			t.Fatalf("talm helper missing %q:\n%s", want, helperText)
		}
	}
	for _, unwanted := range []string{`include "talm.config.network.multidoc"`, "kind: LinkConfig", "2605:6440", `talm.discovered.system_disk_name`, `disk: {{`} {
		if strings.Contains(helperText, unwanted) {
			t.Fatalf("talm helper still contains %q:\n%s", unwanted, helperText)
		}
	}
	raw, err := os.ReadFile(result.GenesisBundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "age-encryption.org/v1") {
		t.Fatalf("genesis bundle is not age encrypted")
	}
	platformRaw, err := os.ReadFile(filepath.Join(result.StateDir, "cozystack-platform.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	platformText := string(platformRaw)
	for _, want := range []string{
		"kind: Package",
		"name: cozystack.cozystack-platform",
		"variant: isp-full",
		"host: \"\"",
		"exposedServices: []",
		"podCIDR: 10.244.0.0/16",
		"podGateway: 10.244.0.1",
		"serviceCIDR: 10.96.0.0/16",
		"joinCIDR: 100.64.0.0/16",
		"apiServerEndpoint: https://206.223.228.87:6443",
		"MASTER_NODES: 206.223.228.87",
	} {
		if !strings.Contains(platformText, want) {
			t.Fatalf("cozystack platform missing %q:\n%s", want, platformText)
		}
	}
}

func TestRunExecutePrunesUnchangedStages(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateRoot)
	loaded := testLoaded()
	layout, err := state.Open(loaded.Config.Cluster.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(layout.TalmProject, "secrets.yaml"),
		filepath.Join(layout.TalmProject, "talm.key"),
		layout.Talosconfig,
		layout.Kubeconfig,
		layout.GenesisArchive,
	} {
		if err := state.WriteFile(path, []byte("state")); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.WriteFile(layout.TalmValues, []byte("advertisedSubnets: []\n")); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteFile(filepath.Join(layout.TalmProject, "templates", "_helpers.tpl"), []byte(`{{- define "talos.config.network.multidoc" }}
noop
{{- end }}
`)); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteOperation(layout.Operation, state.Operation{
		ClusterName:  loaded.Config.Cluster.Name,
		ConfigDigest: loaded.Digest,
		Stage:        "kubectl-wait-cozystack-helmreleases",
		UpdatedAt:    time.Date(2026, 6, 18, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		output:                 []byte("ok"),
		talosConfigured:        true,
		kubernetesBootstrapped: true,
		cozystackConverged:     true,
	}
	status := &fakeStatusReporter{}

	result := Run(context.Background(), loaded, testTools(), runner, Options{
		Execute: true,
		Status:  status,
		Now:     func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
	})
	if result.Outcome != "Converged" {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
	}
	got := runner.names()
	for _, unwanted := range []string{
		"boot-to-talos-install",
		"wait-talos-maintenance-api",
		"talm-dry-run",
		"talm-apply",
		"wait-talos-api",
		"talm-bootstrap",
		"talm-kubeconfig",
		"kubectl-wait-kubernetes-api",
		"kubectl-wait-node-registered",
		"write-genesis-bundle",
		"kubectl-remove-control-plane-taint",
		"write-cozystack-platform",
		"helm-install-cozystack",
		"kubectl-wait-cozystack-operator",
		"kubectl-apply-cozystack-platform",
		"kubectl-wait-platform-package",
		"kubectl-wait-node-ready",
		"kubectl-wait-cozystack-helmreleases",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("commands = %s, want %s pruned", got, unwanted)
		}
	}
	if !status.contains(upStatusMatch{ID: "talos", State: StatusUnchanged, Description: "Already installed"}) {
		t.Fatalf("missing unchanged Talos event: %#v", status.events)
	}
	if !status.contains(upStatusMatch{ID: "kubernetes", State: StatusUnchanged, Description: "Already bootstrapped"}) {
		t.Fatalf("missing unchanged Kubernetes event: %#v", status.events)
	}
	if !status.contains(upStatusMatch{ID: "cozystack", State: StatusUnchanged, Description: "Already converged"}) {
		t.Fatalf("missing unchanged Cozystack event: %#v", status.events)
	}
}

func TestNormalizeNodeConfigPinsDiskSerialAndHostname(t *testing.T) {
	raw := []byte(`machine:
  install:
    disk: /dev/nvme0n1
    image: ghcr.io/cozystack/cozystack/talos:v1.13.0
---
apiVersion: v1alpha1
kind: HostnameConfig
hostname: talos-random
---
apiVersion: v1alpha1
kind: ResolverConfig
nameservers: []
---
apiVersion: v1alpha1
kind: LinkConfig
name: enp1s0f1
addresses:
  - address: 206.223.228.87/31
  - address: 2605:6440:d000:1e0:925a:8ff:fe33:ba9f/64
routes:
  - gateway: 206.223.228.86
  - gateway: fe80::1
`)
	cfg := testLoaded().Config

	got, err := normalizeNodeConfig(raw, cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		"diskSelector:",
		"serial: 362510FE3218",
		"hostname: gi-ash-bm-004",
		"nameservers:",
		"address: 1.1.1.1",
		"address: 8.8.8.8",
		"deviceSelector:",
		"hardwareAddr: 90:5a:08:33:bb:99",
		"dhcp: false",
		"addresses:",
		"- 206.223.228.87/31",
		"network: 0.0.0.0/0",
		"gateway: 206.223.228.86",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("normalized config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "disk: /dev/nvme0n1") {
		t.Fatalf("normalized config still selects disk by device path:\n%s", text)
	}
	for _, unwanted := range []string{"kind: LinkConfig", "2605:6440", "fe80::1"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("normalized config still contains %q:\n%s", unwanted, text)
		}
	}
}

func TestNormalizeNodeConfigLeavesPlainOutputAlone(t *testing.T) {
	raw := []byte("machine config")
	got, err := normalizeNodeConfig(raw, testLoaded().Config)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("normalizeNodeConfig changed plain output: %q", got)
	}
}

type fakeRunner struct {
	commands               []toolrunner.Command
	output                 []byte
	outputErr              error
	talosConfigured        bool
	kubernetesBootstrapped bool
	cozystackConverged     bool
}

func (r *fakeRunner) Run(_ context.Context, cmd toolrunner.Command) error {
	r.commands = append(r.commands, cmd)
	switch cmd.Name {
	case "talm-init":
		if err := os.MkdirAll(cmd.Dir, 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(cmd.Dir, "templates"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cmd.Dir, "values.yaml"), []byte("advertisedSubnets: []\n"), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cmd.Dir, "templates", "_helpers.tpl"), []byte(`{{- define "something.else" }}
noop
{{- end }}

{{- define "talos.config.machine.common" }}
machine:
  install:
    {{- with .Values.image }}
    image: {{ . }}
    {{- end }}
    {{- (include "talm.discovered.disks_info" .) | nindent 4 }}
    disk: {{ include "talm.discovered.system_disk_name" . | quote }}
{{- end }}

{{- define "talos.config.network.multidoc" }}
{{- include "talm.config.network.multidoc" . }}
{{- end }}

{{- define "talos.config.multidoc" }}
{{- include "talos.config.network.multidoc" . }}
{{- end }}
`), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cmd.Dir, "talm.key"), []byte("talm key"), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cmd.Dir, "talosconfig"), []byte("talosconfig"), 0o600); err != nil {
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
	switch cmd.Name {
	case "talos-version":
		if r.talosConfigured {
			return r.output, nil
		}
		return nil, errors.New("talos api not configured")
	case "kubectl-probe-kubernetes-api", "kubectl-probe-node-registered":
		if r.kubernetesBootstrapped {
			return r.output, nil
		}
		return nil, errors.New("kubernetes api not configured")
	case "helm-probe-cozystack":
		if r.cozystackConverged {
			return []byte(`{"chart":"cozy-installer-1.4.1","info":{"status":"deployed"}}`), nil
		}
		return nil, errors.New("cozystack helm release not ready")
	case "kubectl-probe-cozystack-operator", "kubectl-probe-cozystack-platform", "kubectl-probe-node-ready":
		if r.cozystackConverged {
			return r.output, nil
		}
		return nil, errors.New("cozystack not ready")
	case "kubectl-probe-cozystack-helmreleases":
		if r.cozystackConverged {
			return readyHelmReleaseList(), nil
		}
		return nil, errors.New("cozystack helmreleases not ready")
	case "kubectl-wait-cozystack-helmreleases":
		return readyHelmReleaseList(), nil
	}
	return r.output, r.outputErr
}

func (r *fakeRunner) names() string {
	var names []string
	for _, cmd := range r.commands {
		names = append(names, cmd.Name)
	}
	return strings.Join(names, ",")
}

func readyHelmReleaseList() []byte {
	return []byte(`{"items":[` + strings.TrimSuffix(strings.Repeat(`{"status":{"conditions":[{"type":"Ready","status":"True"}]}},`, 20), ",") + `]}`)
}

type fakeStatusReporter struct {
	events []StatusEvent
}

func (r *fakeStatusReporter) Report(event StatusEvent) {
	r.events = append(r.events, event)
}

type upStatusMatch struct {
	ID          string
	State       StatusState
	Description string
}

func (r *fakeStatusReporter) contains(match upStatusMatch) bool {
	for _, event := range r.events {
		if event.ID == match.ID && event.State == match.State && event.Description == match.Description {
			return true
		}
	}
	return false
}

func flattenArgs(commands []toolrunner.Command) []string {
	var out []string
	for _, cmd := range commands {
		out = append(out, cmd.Args...)
	}
	return out
}

func commandNames(commands []toolrunner.Command) string {
	var names []string
	for _, cmd := range commands {
		names = append(names, cmd.Name)
	}
	return strings.Join(names, ",")
}

func testLoaded() *config.Loaded {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	cfg := config.Config{
		Cluster: config.ClusterSpec{
			Name:           "guardian-dev",
			Endpoint:       "https://206.223.228.87:6443",
			Domain:         "guardianintelligence.org",
			PodCIDR:        "10.244.0.0/16",
			ServiceCIDR:    "10.96.0.0/16",
			JoinCIDR:       "100.64.0.0/16",
			AdvertisedCIDR: "206.223.228.86/31",
		},
		Node: config.NodeSpec{
			Name:              "ash-bm-004",
			Address:           "206.223.228.87",
			Gateway:           "206.223.228.86",
			PrefixLength:      31,
			InterfaceName:     "eno1",
			Hostname:          "gi-ash-bm-004",
			InterfaceMAC:      "90:5a:08:33:bb:99",
			InstallDiskSerial: "362510FE3218",
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
			PlatformVariant:    "isp-full",
			PublishingHost:     "",
			ExposedServices:    []string{},
			RemoveControlTaint: true,
		},
		Bootstrap: config.BootstrapSpec{
			Destructive:        true,
			RequireMaintenance: true,
			TargetState:        "stock-ubuntu",
			Genesis: config.GenesisSpec{
				AgeRecipients: []string{identity.Recipient().String()},
			},
		},
	}
	return &config.Loaded{Path: "src/hosts/ash-bm-004/host.cue", Config: cfg, Digest: "digest"}
}

func testTools() Tools {
	return Tools{
		Talm:        "/runfiles/talm",
		Talos:       "/runfiles/talosctl",
		Kubectl:     "/runfiles/kubectl",
		Helm:        "/runfiles/helm",
		BootToTalos: "/runfiles/boot-to-talos",
	}
}
