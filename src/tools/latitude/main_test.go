package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerIDForNode(t *testing.T) {
	dir := t.TempDir()
	inventoryPath := filepath.Join(dir, "inventory.json")
	if err := os.WriteFile(inventoryPath, []byte(`{
  "nodes": [
    {"name": "ash-earth", "server_id": "sv_earth"},
    {"name": "ash-wind", "server_id": "sv_wind"}
  ]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := serverIDForNode(inventoryPath, "ash-wind")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sv_wind" {
		t.Fatalf("serverIDForNode() = %q, want sv_wind", got)
	}
}

func TestServerIDForNodeMissing(t *testing.T) {
	dir := t.TempDir()
	inventoryPath := filepath.Join(dir, "inventory.json")
	if err := os.WriteFile(inventoryPath, []byte(`{"nodes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := serverIDForNode(inventoryPath, "ash-water"); err == nil {
		t.Fatal("serverIDForNode() succeeded for missing node")
	}
}

func TestParseOptions(t *testing.T) {
	opts, err := parseOptions([]string{
		"--action", "power_off",
		"--node", "ash-earth",
		"--timeout", "2m",
		"--poll-interval", "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.action != "power_off" {
		t.Fatalf("action = %q, want power_off", opts.action)
	}
	if opts.node != "ash-earth" {
		t.Fatalf("node = %q, want ash-earth", opts.node)
	}
	if opts.timeout != 2*time.Minute {
		t.Fatalf("timeout = %s, want 2m", opts.timeout)
	}
	if opts.pollInterval != 5*time.Second {
		t.Fatalf("pollInterval = %s, want 5s", opts.pollInterval)
	}
}

func TestParseOptionsRejectsUnsupportedAction(t *testing.T) {
	if _, err := parseOptions([]string{"--action", "destroy"}); err == nil {
		t.Fatal("parseOptions() accepted unsupported action")
	}
}
