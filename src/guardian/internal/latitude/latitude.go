package latitude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.latitude.sh"

var ErrServerBeingProvisioned = errors.New("latitude server is being provisioned")

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type Server struct {
	ID          string
	Hostname    string
	PrimaryIPv4 string
	Status      string
	Locked      bool
	Project     string
	OS          string
}

func (c Client) GetServer(ctx context.Context, serverID string) (Server, error) {
	var out serverResponse
	if err := c.do(ctx, http.MethodGet, "/servers/"+serverID, nil, &out, http.StatusOK); err != nil {
		return Server{}, err
	}
	return Server{
		ID:          out.Data.ID,
		Hostname:    out.Data.Attributes.Hostname,
		PrimaryIPv4: out.Data.Attributes.PrimaryIPv4,
		Status:      out.Data.Attributes.Status,
		Locked:      out.Data.Attributes.Locked,
		Project:     out.Data.Attributes.Project.Name,
		OS:          out.Data.Attributes.OperatingSystem.Slug,
	}, nil
}

func (c Client) ReinstallIPXE(ctx context.Context, serverID, hostname, ipxeURL string) error {
	body := map[string]any{
		"data": map[string]any{
			"type": "reinstalls",
			"attributes": map[string]any{
				"operating_system": "ipxe",
				"hostname":         hostname,
				"ipxe":             ipxeURL,
			},
		},
	}
	return c.do(ctx, http.MethodPost, "/servers/"+serverID+"/reinstall", body, nil, http.StatusOK, http.StatusCreated, http.StatusAccepted)
}

func (c Client) do(ctx context.Context, method, path string, body any, out any, wantStatuses ...int) error {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("latitude token is empty")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode latitude request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.api+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/vnd.api+json")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("latitude %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("latitude %s %s: read response: %w", method, path, err)
	}
	if !statusAllowed(resp.StatusCode, wantStatuses) {
		if resp.StatusCode == http.StatusUnprocessableEntity && strings.Contains(string(payload), "SERVER_BEING_PROVISIONED") {
			return fmt.Errorf("%w: %s", ErrServerBeingProvisioned, strings.TrimSpace(string(payload)))
		}
		return fmt.Errorf("latitude %s %s: got %s: %s", method, path, resp.Status, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("latitude %s %s: decode response: %w", method, path, err)
	}
	return nil
}

func statusAllowed(got int, want []int) bool {
	for _, status := range want {
		if got == status {
			return true
		}
	}
	return false
}

type serverResponse struct {
	Data struct {
		ID         string `json:"id"`
		Attributes struct {
			Hostname    string `json:"hostname"`
			PrimaryIPv4 string `json:"primary_ipv4"`
			Status      string `json:"status"`
			Locked      bool   `json:"locked"`
			Project     struct {
				Name string `json:"name"`
			} `json:"project"`
			OperatingSystem struct {
				Slug string `json:"slug"`
			} `json:"operating_system"`
		} `json:"attributes"`
	} `json:"data"`
}
