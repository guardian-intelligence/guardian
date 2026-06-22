package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultAPIBase = "https://api.latitude.sh"

type inventory struct {
	Nodes []inventoryNode `json:"nodes"`
}

type inventoryNode struct {
	Name     string `json:"name"`
	ServerID string `json:"server_id"`
}

type actionRequest struct {
	Data actionData `json:"data"`
}

type actionData struct {
	Type       string           `json:"type"`
	Attributes actionAttributes `json:"attributes"`
}

type actionAttributes struct {
	Action string `json:"action"`
}

type serverResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Hostname string `json:"hostname"`
			Status   string `json:"status"`
		} `json:"attributes"`
	} `json:"data"`
}

type outputEvent struct {
	Node       string    `json:"node,omitempty"`
	ServerID   string    `json:"server_id"`
	Action     string    `json:"action"`
	Status     string    `json:"status,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

type options struct {
	action       string
	apiBase      string
	inventory    string
	node         string
	pollInterval time.Duration
	serverID     string
	timeout      time.Duration
	waitStatus   string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "latitude-power: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}

	serverID := opts.serverID
	if serverID == "" {
		serverID, err = serverIDForNode(opts.inventory, opts.node)
		if err != nil {
			return err
		}
	}
	if serverID == "" {
		return errors.New("pass --server-id or --node")
	}

	token := latitudeToken()
	if token == "" {
		return errors.New("set LATITUDESH_AUTH_TOKEN or LATITUDESH_BEARER")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	if opts.action != "status" {
		status, err := createAction(ctx, client, opts.apiBase, token, serverID, opts.action)
		if err != nil {
			return err
		}
		if err := writeEvent(stdout, outputEvent{
			Node:       opts.node,
			ServerID:   serverID,
			Action:     opts.action,
			HTTPStatus: status,
			Timestamp:  time.Now().UTC(),
		}); err != nil {
			return err
		}
	}

	waitStatus := opts.waitStatus
	if waitStatus == "" {
		switch opts.action {
		case "power_off":
			waitStatus = "off"
		case "power_on":
			waitStatus = "on"
		}
	}

	status, err := readStatus(ctx, client, opts.apiBase, token, serverID, waitStatus, opts.timeout, opts.pollInterval)
	if err != nil {
		return err
	}
	return writeEvent(stdout, outputEvent{
		Node:      opts.node,
		ServerID:  serverID,
		Action:    "status",
		Status:    status,
		Timestamp: time.Now().UTC(),
	})
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("latitude-power", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	opts := options{}
	fs.StringVar(&opts.action, "action", "status", "Latitude action: status, power_on, power_off, or reboot")
	fs.StringVar(&opts.apiBase, "api-base", defaultAPIBase, "Latitude API base URL")
	fs.StringVar(&opts.inventory, "inventory", "src/infrastructure/inventory/guardian-mgmt.json", "management inventory JSON")
	fs.StringVar(&opts.node, "node", "", "management node name from inventory")
	fs.DurationVar(&opts.pollInterval, "poll-interval", 10*time.Second, "status poll interval")
	fs.StringVar(&opts.serverID, "server-id", "", "Latitude server ID")
	fs.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "status wait timeout")
	fs.StringVar(&opts.waitStatus, "wait-status", "", "optional status to wait for")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	opts.apiBase = strings.TrimRight(opts.apiBase, "/")
	if err := validateAction(opts.action); err != nil {
		return opts, err
	}
	if opts.pollInterval <= 0 {
		return opts, errors.New("--poll-interval must be positive")
	}
	if opts.timeout <= 0 {
		return opts, errors.New("--timeout must be positive")
	}
	return opts, nil
}

func validateAction(action string) error {
	switch action {
	case "status", "power_on", "power_off", "reboot":
		return nil
	default:
		return fmt.Errorf("unsupported --action %q", action)
	}
}

func serverIDForNode(path, node string) (string, error) {
	if node == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var inv inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return "", err
	}
	for _, candidate := range inv.Nodes {
		if candidate.Name == node {
			if candidate.ServerID == "" {
				return "", fmt.Errorf("node %q has no server_id", node)
			}
			return candidate.ServerID, nil
		}
	}
	return "", fmt.Errorf("node %q not found in %s", node, path)
}

func createAction(ctx context.Context, client *http.Client, apiBase, token, serverID, action string) (int, error) {
	body, err := json.Marshal(actionRequest{
		Data: actionData{
			Type:       "actions",
			Attributes: actionAttributes{Action: action},
		},
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/servers/"+serverID+"/actions", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/vnd.api+json")
	req.Header.Set("Accept", "application/vnd.api+json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("Latitude action %q failed with HTTP %d: %s", action, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.StatusCode, nil
}

func readStatus(ctx context.Context, client *http.Client, apiBase, token, serverID, waitStatus string, timeout, pollInterval time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := getServerStatus(ctx, client, apiBase, token, serverID)
		if err != nil {
			return "", err
		}
		if waitStatus == "" || status == waitStatus {
			return status, nil
		}
		if time.Now().Add(pollInterval).After(deadline) {
			return "", fmt.Errorf("timed out waiting for server %s status %q; last status %q", serverID, waitStatus, status)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func getServerStatus(ctx context.Context, client *http.Client, apiBase, token, serverID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/servers/"+serverID, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.api+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("Latitude status failed with HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded serverResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", err
	}
	if decoded.Data.Attributes.Status == "" {
		return "", errors.New("Latitude status response did not include data.attributes.status")
	}
	return decoded.Data.Attributes.Status, nil
}

func latitudeToken() string {
	for _, key := range []string{"LATITUDESH_AUTH_TOKEN", "LATITUDESH_BEARER"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func writeEvent(w io.Writer, event outputEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(encoded))
	return err
}
