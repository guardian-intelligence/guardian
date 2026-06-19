package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type schematicResponse struct {
	ID string `json:"id"`
}

// registerSchematic uploads the schematic to the Talos Image Factory and
// returns its ID. The factory is content-addressed: POSTing identical
// contents always returns the same ID, so registration is idempotent.
func registerSchematic(path string) (string, error) {
	body, err := os.ReadFile(resolvePath(path))
	if err != nil {
		return "", fmt.Errorf("schematic: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(factoryURL+"/schematics", "application/x-yaml", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("schematic: register with image factory: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("schematic: read factory response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("schematic: factory returned %s: %s", resp.Status, payload)
	}
	var sr schematicResponse
	if err := json.Unmarshal(payload, &sr); err != nil {
		return "", fmt.Errorf("schematic: decode factory response %q: %w", payload, err)
	}
	if sr.ID == "" {
		return "", fmt.Errorf("schematic: factory response missing id: %s", payload)
	}
	return sr.ID, nil
}

func installerImage(schematicID string) string {
	return fmt.Sprintf("factory.talos.dev/installer/%s:%s", schematicID, talosVersion)
}
