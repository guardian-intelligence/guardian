package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCUEConfigWithDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.cue")
	if err := os.WriteFile(path, []byte(validCue()), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest == "" {
		t.Fatal("digest is empty")
	}
	if loaded.Config.Talm.Preset != "cozystack" {
		t.Fatalf("preset = %q, want cozystack", loaded.Config.Talm.Preset)
	}
	if loaded.Config.Cluster.PodCIDR != "10.244.0.0/16" {
		t.Fatalf("pod cidr = %q", loaded.Config.Cluster.PodCIDR)
	}
}

func TestLoadRejectsMissingDestructiveFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.cue")
	if err := os.WriteFile(path, []byte(`package cluster
cluster: name: "guardian-dev"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded, want missing-field error")
	}
}

func validCue() string {
	return `package cluster

cluster: {
	name: "guardian-dev"
	endpoint: "https://206.223.228.101:6443"
	domain: "guardianintelligence.org"
	advertisedCIDR: "206.223.228.100/31"
}
node: {
	name: "ash-bm-001"
	address: "206.223.228.101"
	hostname: "gi-ash-bm-001"
	interfaceMac: "90:5a:08:33:ba:9f"
	installDiskSerial: "362510FCEFB8"
}
talm: {
	talosVersion: "v1.13"
	kubernetesVersion: "1.36.1"
	installerImage: "ghcr.io/cozystack/cozystack/talos:v1.13.0"
}
cozystack: {
	version: "1.4.1"
	publishingHost: "dev.guardianintelligence.org"
	apiServerEndpoint: "https://api.dev.guardianintelligence.org:443"
}
bootstrap: {
	destructive: true
	requireMaintenance: true
}
`
}
