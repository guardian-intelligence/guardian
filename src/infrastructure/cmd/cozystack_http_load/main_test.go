package main

import (
	"reflect"
	"testing"
)

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name   string
		cfg    loadConfig
		port   int
		url    string
		status string
	}{
		{
			name:   "harbor root",
			cfg:    loadConfig{Surface: "harbor", Stage: "root"},
			url:    "https://harbor.guardianintelligence.org/v2/",
			status: "200,401",
		},
		{
			name:   "dashboard root",
			cfg:    loadConfig{Surface: "dashboard", Stage: "root"},
			url:    "https://dashboard.guardianintelligence.org/",
			status: "200,302",
		},
		{
			name:   "openbao root with port",
			cfg:    loadConfig{Surface: "openbao", Stage: "root"},
			port:   18200,
			url:    "http://127.0.0.1:18200/v1/sys/health",
			status: "200,429,472,473,501,503",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTarget(tt.cfg, tt.port)
			if err != nil {
				t.Fatalf("resolveTarget() error = %v", err)
			}
			if got.URL != tt.url {
				t.Fatalf("URL = %q, want %q", got.URL, tt.url)
			}
			if got.ExpectedStatuses != tt.status {
				t.Fatalf("ExpectedStatuses = %q, want %q", got.ExpectedStatuses, tt.status)
			}
		})
	}
}

func TestResolveTargetValidation(t *testing.T) {
	for _, cfg := range []loadConfig{
		{Surface: "harbor", Stage: "gamma"},
		{Surface: "dashboard", Stage: "dev"},
		{Surface: "openbao", Stage: "dev"},
		{Surface: "nope", Stage: "dev"},
	} {
		if _, err := resolveTarget(cfg, 0); err == nil {
			t.Fatalf("resolveTarget(%#v) accepted invalid target", cfg)
		}
	}

	got, err := resolveTarget(loadConfig{Surface: "openbao", Stage: "root"}, 0)
	if err != nil {
		t.Fatalf("openbao without port should prepare a port-forward target: %v", err)
	}
	if !got.NeedsOpenBaoPort {
		t.Fatalf("openbao without local port did not request port-forward: %#v", got)
	}
}

func TestOpenBaoPortForwardArgs(t *testing.T) {
	cfg := loadConfig{Kubeconfig: "/kubeconfig", RequestTimeout: "15s"}
	want := []string{
		"--kubeconfig", "/kubeconfig",
		"--request-timeout=15s",
		"-n", "tenant-guardian",
		"port-forward",
		"--address", "127.0.0.1",
		"svc/guardian-openbao",
		"18200:8200",
	}
	if got := openBaoPortForwardArgs(cfg, 18200); !reflect.DeepEqual(got, want) {
		t.Fatalf("openBaoPortForwardArgs() = %#v, want %#v", got, want)
	}
}

func TestValidateConfig(t *testing.T) {
	base := loadConfig{
		K6:                   "/k6",
		Script:               "http-smoke.js",
		Kubectl:              "/kubectl",
		Surface:              "harbor",
		Stage:                "root",
		VUs:                  "1",
		Duration:             "1s",
		SleepSeconds:         "1",
		WaitTimeout:          "15m",
		PortForwardReadyWait: 1,
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	missingTarget := base
	missingTarget.Surface = ""
	if err := validateConfig(missingTarget); err == nil {
		t.Fatalf("config without surface or URL was accepted")
	}

	customURL := missingTarget
	customURL.Kubectl = ""
	customURL.WaitTimeout = ""
	customURL.URL = "https://example.invalid/healthz"
	if err := validateConfig(customURL); err != nil {
		t.Fatalf("custom URL config rejected: %v", err)
	}

	hostOverride := base
	hostOverride.HostOverrides = "harbor.guardianintelligence.org=45.250.254.119"
	if err := validateConfig(hostOverride); err != nil {
		t.Fatalf("config with host override rejected: %v", err)
	}

	missingKubectl := base
	missingKubectl.Kubectl = ""
	if err := validateConfig(missingKubectl); err == nil {
		t.Fatalf("built-in surface without kubectl was accepted")
	}

	badVUs := base
	badVUs.VUs = "many"
	if err := validateConfig(badVUs); err == nil {
		t.Fatalf("non-numeric VUs accepted")
	}

	badHostOverride := base
	badHostOverride.HostOverrides = "harbor.guardianintelligence.org=not-an-ip"
	if err := validateConfig(badHostOverride); err == nil {
		t.Fatalf("invalid host override accepted")
	}
}

func TestNormalizeHostOverrides(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "empty",
			input: "",
			want:  "",
		},
		{
			name:  "normalizes spacing case and order",
			input: " Harbor.GuardianIntelligence.Org = 45.250.254.119, dashboard.guardianintelligence.org=206.223.228.87 ",
			want:  "dashboard.guardianintelligence.org=206.223.228.87,harbor.guardianintelligence.org=45.250.254.119",
		},
		{
			name:  "ipv6 literal",
			input: "example.com=2001:db8::1",
			want:  "example.com=2001:db8::1",
		},
		{
			name:    "missing separator",
			input:   "harbor.guardianintelligence.org:45.250.254.119",
			wantErr: true,
		},
		{
			name:    "host must not include port",
			input:   "harbor.guardianintelligence.org:443=45.250.254.119",
			wantErr: true,
		},
		{
			name:    "invalid host",
			input:   "bad_host=45.250.254.119",
			wantErr: true,
		},
		{
			name:    "invalid ip",
			input:   "harbor.guardianintelligence.org=not-an-ip",
			wantErr: true,
		},
		{
			name:    "duplicate host",
			input:   "harbor.guardianintelligence.org=206.223.228.87,harbor.guardianintelligence.org=45.250.254.119",
			wantErr: true,
		},
		{
			name:    "trailing comma",
			input:   "harbor.guardianintelligence.org=206.223.228.87,",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeHostOverrides(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeHostOverrides(%q) accepted invalid input", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeHostOverrides(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeHostOverrides(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSurfaceReadinessChecks(t *testing.T) {
	tests := []struct {
		name     string
		cfg      loadConfig
		required []commandExpectation
	}{
		{
			name: "harbor",
			cfg:  loadConfig{Surface: "harbor", Stage: "root", WaitTimeout: "20m"},
			required: []commandExpectation{
				{label: "Harbor app yaml", parts: []string{"tenant-root", "harbors.apps.cozystack.io/guardian", "-o", "yaml"}},
				{label: "wait Harbor app Ready", parts: []string{"--for=condition=Ready", "harbors.apps.cozystack.io/guardian", "--timeout=20m"}},
				{label: "wait Harbor workloads Ready", parts: []string{"--for=condition=WorkloadsReady", "harbors.apps.cozystack.io/guardian", "--timeout=20m"}},
			},
		},
		{
			name: "dashboard",
			cfg:  loadConfig{Surface: "dashboard", Stage: "root", WaitTimeout: "20m"},
			required: []commandExpectation{
				{label: "dashboard console deployment yaml", parts: []string{"cozy-dashboard", "deployment/cozy-dashboard-console", "-o", "yaml"}},
				{label: "dashboard gatekeeper deployment yaml", parts: []string{"cozy-dashboard", "deployment/incloud-web-gatekeeper", "-o", "yaml"}},
				{label: "wait dashboard console deployment Available", parts: []string{"--for=condition=Available", "deployment/cozy-dashboard-console", "--timeout=20m"}},
				{label: "wait dashboard gatekeeper deployment Available", parts: []string{"--for=condition=Available", "deployment/incloud-web-gatekeeper", "--timeout=20m"}},
			},
		},
		{
			name: "openbao",
			cfg:  loadConfig{Surface: "openbao", Stage: "root", WaitTimeout: "20m"},
			required: []commandExpectation{
				{label: "OpenBao HelmRelease yaml", parts: []string{"tenant-guardian", "helmrelease.helm.toolkit.fluxcd.io/guardian-openbao", "-o", "yaml"}},
				{label: "OpenBao statefulset yaml", parts: []string{"tenant-guardian", "statefulset.apps/guardian-openbao", "-o", "yaml"}},
				{label: "wait OpenBao HelmRelease Ready", parts: []string{"--for=condition=Ready", "helmrelease.helm.toolkit.fluxcd.io/guardian-openbao", "--timeout=20m"}},
				{label: "wait OpenBao statefulset ready replicas", parts: []string{"--for=jsonpath={.status.readyReplicas}=3", "statefulset.apps/guardian-openbao", "--timeout=20m"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := surfaceReadinessChecks(tt.cfg)
			if err != nil {
				t.Fatalf("surfaceReadinessChecks() error = %v", err)
			}
			for _, want := range tt.required {
				requireCommand(t, got, want.label, want.parts...)
			}
		})
	}
}

func TestSurfaceReadinessValidation(t *testing.T) {
	for _, cfg := range []loadConfig{
		{Surface: "dashboard", Stage: "dev", WaitTimeout: "15m"},
		{Surface: "openbao", Stage: "dev", WaitTimeout: "15m"},
		{Surface: "custom", Stage: "dev", WaitTimeout: "15m"},
		{Surface: "nope", Stage: "dev", WaitTimeout: "15m"},
	} {
		if _, err := surfaceReadinessChecks(cfg); err == nil {
			t.Fatalf("surfaceReadinessChecks(%#v) accepted invalid target", cfg)
		}
	}
}

func TestKubectlBaseArgs(t *testing.T) {
	got := kubectlBaseArgs(loadConfig{
		Kubeconfig:     "/tmp/kubeconfig",
		RequestTimeout: "5s",
	})
	want := []string{"--kubeconfig", "/tmp/kubeconfig", "--request-timeout=5s"}
	if len(got) != len(want) {
		t.Fatalf("kubectlBaseArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kubectlBaseArgs[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
}

func TestKubectlArgs(t *testing.T) {
	got := kubectlArgs(loadConfig{
		Kubeconfig:     "/tmp/kubeconfig",
		RequestTimeout: "5s",
	}, "get", "nodes")
	want := []string{"--kubeconfig", "/tmp/kubeconfig", "--request-timeout=5s", "get", "nodes"}
	if len(got) != len(want) {
		t.Fatalf("kubectlArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kubectlArgs[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
}

type commandExpectation struct {
	label string
	parts []string
}

func requireCommand(t *testing.T, checks []kubectlCommand, label string, parts ...string) {
	t.Helper()
	for _, check := range checks {
		if check.Label != label {
			continue
		}
		for _, part := range parts {
			if !hasArg(check.Args, part) {
				t.Fatalf("%s missing arg %q: %#v", label, part, check.Args)
			}
		}
		return
	}
	t.Fatalf("missing command %q in %#v", label, checks)
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
