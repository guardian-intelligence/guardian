package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxSpiceDBResponseBytes = 1 << 20

type objectRef struct {
	Type string `json:"objectType"`
	ID   string `json:"objectId"`
}

type subjectRef struct {
	Object           objectRef `json:"object"`
	OptionalRelation string    `json:"optionalRelation,omitempty"`
}

type checkInput struct {
	Resource            objectRef
	Permission          string
	Subject             subjectRef
	AtLeastAsFreshToken string
}

type checkResult struct {
	Allowed bool
	Token   string
}

type relationshipUpdate struct {
	Operation string
	Resource  objectRef
	Relation  string
	Subject   subjectRef
}

type spiceDBClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func (c *spiceDBClient) check(ctx context.Context, input checkInput) (checkResult, error) {
	consistency := map[string]any{"minimizeLatency": true}
	if input.AtLeastAsFreshToken != "" {
		consistency = map[string]any{
			"atLeastAsFresh": map[string]string{"token": input.AtLeastAsFreshToken},
		}
	}
	body := map[string]any{
		"resource":    input.Resource,
		"permission":  input.Permission,
		"subject":     input.Subject,
		"consistency": consistency,
	}
	var response struct {
		Permissionship string `json:"permissionship"`
		CheckedAt      struct {
			Token string `json:"token"`
		} `json:"checkedAt"`
	}
	if err := c.post(ctx, "/v1/permissions/check", body, &response); err != nil {
		return checkResult{}, err
	}
	switch response.Permissionship {
	case "PERMISSIONSHIP_HAS_PERMISSION":
		return checkResult{Allowed: true, Token: response.CheckedAt.Token}, nil
	case "PERMISSIONSHIP_NO_PERMISSION":
		return checkResult{Allowed: false, Token: response.CheckedAt.Token}, nil
	default:
		return checkResult{}, fmt.Errorf("SpiceDB returned permissionship %q", response.Permissionship)
	}
}

func (c *spiceDBClient) write(ctx context.Context, updates []relationshipUpdate) (string, error) {
	wireUpdates := make([]map[string]any, 0, len(updates))
	for _, update := range updates {
		wireUpdates = append(wireUpdates, map[string]any{
			"operation": update.Operation,
			"relationship": map[string]any{
				"resource": update.Resource,
				"relation": update.Relation,
				"subject":  update.Subject,
			},
		})
	}
	var response struct {
		WrittenAt struct {
			Token string `json:"token"`
		} `json:"writtenAt"`
	}
	if err := c.post(ctx, "/v1/relationships/write", map[string]any{"updates": wireUpdates}, &response); err != nil {
		return "", err
	}
	if response.WrittenAt.Token == "" {
		return "", errors.New("SpiceDB write response lacked a revision token")
	}
	return response.WrittenAt.Token, nil
}

func (c *spiceDBClient) post(ctx context.Context, path string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("SpiceDB request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("SpiceDB responded with HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxSpiceDBResponseBytes)).Decode(output); err != nil {
		return fmt.Errorf("decode SpiceDB response: %w", err)
	}
	return nil
}
