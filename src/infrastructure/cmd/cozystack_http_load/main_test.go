package main

import "testing"

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name   string
		cfg    loadConfig
		port   int
		url    string
		status string
	}{
		{
			name:   "company dev",
			cfg:    loadConfig{Surface: "company-site", Stage: "dev"},
			url:    "https://dev.gi.org/healthz",
			status: "200",
		},
		{
			name:   "company prod",
			cfg:    loadConfig{Surface: "website", Stage: "prod"},
			url:    "https://guardianintelligence.org/healthz",
			status: "200",
		},
		{
			name:   "harbor root",
			cfg:    loadConfig{Surface: "harbor", Stage: "root"},
			url:    "https://harbor.guardianintelligence.org/v2/",
			status: "200,401",
		},
		{
			name:   "harbor gamma",
			cfg:    loadConfig{Surface: "registry", Stage: "gamma"},
			url:    "https://harbor.gamma.gi.org/v2/",
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
		{Surface: "company-site", Stage: "root"},
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

func TestValidateConfig(t *testing.T) {
	base := loadConfig{
		K6:                   "/k6",
		Script:               "http-smoke.js",
		Surface:              "company-site",
		Stage:                "dev",
		VUs:                  "1",
		Duration:             "1s",
		SleepSeconds:         "1",
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
	customURL.URL = "https://example.invalid/healthz"
	if err := validateConfig(customURL); err != nil {
		t.Fatalf("custom URL config rejected: %v", err)
	}

	badVUs := base
	badVUs.VUs = "many"
	if err := validateConfig(badVUs); err == nil {
		t.Fatalf("non-numeric VUs accepted")
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
