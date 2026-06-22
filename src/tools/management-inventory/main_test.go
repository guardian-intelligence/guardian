package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRunNodesLines(t *testing.T) {
	inventoryPath := writeInventory(t)
	var out bytes.Buffer
	if err := run([]string{"--inventory", inventoryPath, "nodes"}, &out); err != nil {
		t.Fatal(err)
	}
	const want = "ash-earth\nash-wind\nash-water\n"
	if out.String() != want {
		t.Fatalf("nodes output = %q, want %q", out.String(), want)
	}
}

func TestRunNodesCSV(t *testing.T) {
	inventoryPath := writeInventory(t)
	var out bytes.Buffer
	if err := run([]string{"--inventory", inventoryPath, "--format", "csv", "nodes"}, &out); err != nil {
		t.Fatal(err)
	}
	const want = "ash-earth,ash-wind,ash-water\n"
	if out.String() != want {
		t.Fatalf("nodes csv output = %q, want %q", out.String(), want)
	}
}

func TestRunPublicIPsCSV(t *testing.T) {
	inventoryPath := writeInventory(t)
	var out bytes.Buffer
	if err := run([]string{"--inventory", inventoryPath, "--format", "csv", "public-ips"}, &out); err != nil {
		t.Fatal(err)
	}
	const want = "206.223.228.101,45.250.254.119,206.223.228.87\n"
	if out.String() != want {
		t.Fatalf("public-ips csv output = %q, want %q", out.String(), want)
	}
}

func TestParseOptionsRejectsUnknownFormat(t *testing.T) {
	if _, _, err := parseOptions([]string{"--format", "json", "nodes"}); err == nil {
		t.Fatal("parseOptions() accepted unsupported format")
	}
}

func writeInventory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.json")
	if err := os.WriteFile(path, []byte(`{
  "nodes": [
    {"name": "ash-earth", "server_id": "sv_earth", "public_ipv4": "206.223.228.101"},
    {"name": "ash-wind", "server_id": "sv_wind", "public_ipv4": "45.250.254.119"},
    {"name": "ash-water", "server_id": "sv_water", "public_ipv4": "206.223.228.87"}
  ]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
