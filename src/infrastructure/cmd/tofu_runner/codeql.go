package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"time"
)

const codeQLRepository = "guardian-intelligence/guardian"
const codeQLRunner = "postflight-4vcpu-ubuntu24-turbo"

// Only these three fields are owned. In particular, languages must not be
// sent back: GitHub can support languages absent from a client's API enum.
type codeQLRouting struct {
	State       string `json:"state"`
	RunnerType  string `json:"runner_type"`
	RunnerLabel string `json:"runner_label"`
}

type codeQLDesired struct {
	Repository        string        `json:"repository"`
	DefaultSetup      codeQLRouting `json:"default_setup"`
	RequiredLanguages []string      `json:"required_languages"`
}

func loadCodeQLDesired(path string) (codeQLDesired, error) {
	var desired codeQLDesired
	file, err := os.Open(path)
	if err != nil {
		return desired, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&desired); err != nil {
		return desired, fmt.Errorf("invalid CodeQL declaration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return desired, errors.New("CodeQL declaration must contain one JSON object")
	}
	if desired.Repository != codeQLRepository || desired.DefaultSetup != (codeQLRouting{
		State: "configured", RunnerType: "labeled", RunnerLabel: codeQLRunner,
	}) {
		return desired, errors.New("CodeQL declaration must select the Guardian Turbo default setup")
	}
	languages := append([]string(nil), desired.RequiredLanguages...)
	sort.Strings(languages)
	if !reflect.DeepEqual(languages, []string{"actions", "python", "rust"}) {
		return desired, errors.New("CodeQL declaration must retain the actions, python, and rust coverage floor")
	}
	return desired, nil
}

type codeQLSetup struct {
	codeQLRouting
	Languages []string
	// All unowned fields are compared after validation, not sent in PATCH.
	Unowned map[string]any
}

func decodeCodeQLSetup(body []byte) (codeQLSetup, error) {
	var setup codeQLSetup
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return setup, errors.New("invalid CodeQL setup response")
	}
	if err := json.Unmarshal(body, &setup.codeQLRouting); err != nil {
		return setup, errors.New("invalid CodeQL routing response")
	}
	var coverage struct {
		Languages []string `json:"languages"`
	}
	if err := json.Unmarshal(body, &coverage); err != nil {
		return setup, errors.New("invalid CodeQL language response")
	}
	setup.Languages = coverage.Languages
	for _, key := range []string{"state", "runner_type", "runner_label", "updated_at"} {
		delete(fields, key)
	}
	// Language ordering is not a coverage change. Preserve all languages,
	// including those GitHub adds beyond this controller's coverage floor.
	ordered := append([]string(nil), setup.Languages...)
	sort.Strings(ordered)
	fields["languages"] = ordered
	setup.Unowned = fields
	return setup, nil
}

func (setup codeQLSetup) assertCoverage(required []string) error {
	for _, language := range required {
		found := false
		for _, current := range setup.Languages {
			found = found || current == language
		}
		if !found {
			return fmt.Errorf("CodeQL coverage is missing required language %s; refusing routing changes", language)
		}
	}
	return nil
}

type codeQLResult struct {
	Status   string
	RunID    int64
	Observed codeQLRouting
}

var errCodeQLPending = errors.New("CodeQL validation remains pending; routing is not verified")

type codeQLClient struct {
	baseURL string
	token   string
	client  *http.Client
	wait    func(context.Context) error
}

func (client codeQLClient) request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("cannot create GitHub CodeQL request")
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.client.Do(req)
	if err != nil {
		// Never log request headers, tokens, or provider response bodies.
		return nil, 0, errors.New("GitHub CodeQL request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, resp.StatusCode, errors.New("invalid GitHub CodeQL response size")
	}
	return data, resp.StatusCode, nil
}

func (client codeQLClient) setup(ctx context.Context) (codeQLSetup, error) {
	body, status, err := client.request(ctx, http.MethodGet, "/repos/"+codeQLRepository+"/code-scanning/default-setup", nil)
	if err != nil {
		return codeQLSetup{}, err
	}
	if status != http.StatusOK {
		return codeQLSetup{}, fmt.Errorf("GitHub CodeQL GET returned HTTP %d", status)
	}
	return decodeCodeQLSetup(body)
}

func (client codeQLClient) reconcile(ctx context.Context, desired codeQLDesired, mode mode) (codeQLResult, error) {
	before, err := client.setup(ctx)
	if err != nil {
		return codeQLResult{}, err
	}
	if err := before.assertCoverage(desired.RequiredLanguages); err != nil {
		return codeQLResult{}, err
	}
	if before.codeQLRouting == desired.DefaultSetup {
		return codeQLResult{Status: "no-op", Observed: before.codeQLRouting}, nil
	}
	if mode != modeApply {
		return codeQLResult{Status: "drift", Observed: before.codeQLRouting}, nil
	}
	body, err := json.Marshal(desired.DefaultSetup)
	if err != nil {
		return codeQLResult{}, err
	}
	body, status, err := client.request(ctx, http.MethodPatch, "/repos/"+codeQLRepository+"/code-scanning/default-setup", body)
	if err != nil {
		return codeQLResult{}, err
	}
	if status == http.StatusConflict {
		// Another configuration has an outstanding validation. Observe once,
		// but do not PATCH again or claim success without its validation ID.
		_, err := client.setup(ctx)
		if err != nil {
			return codeQLResult{Status: "pending"}, err
		}
		return codeQLResult{Status: "pending"}, errCodeQLPending
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return codeQLResult{}, fmt.Errorf("GitHub CodeQL PATCH returned HTTP %d", status)
	}
	var validation struct {
		RunID int64 `json:"run_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &validation); err != nil {
			return codeQLResult{}, errors.New("invalid CodeQL validation response")
		}
	}
	if status == http.StatusAccepted && validation.RunID <= 0 {
		return codeQLResult{Status: "pending"}, errCodeQLPending
	}
	if validation.RunID > 0 {
		if err := client.validation(ctx, validation.RunID); err != nil {
			return codeQLResult{Status: "pending", RunID: validation.RunID}, err
		}
	}
	after, err := client.setup(ctx)
	if err != nil {
		return codeQLResult{}, err
	}
	if err := after.assertCoverage(desired.RequiredLanguages); err != nil {
		return codeQLResult{}, err
	}
	if !reflect.DeepEqual(before.Unowned, after.Unowned) {
		return codeQLResult{}, errors.New("unowned CodeQL settings changed during validation; manual review required")
	}
	if after.codeQLRouting != desired.DefaultSetup {
		return codeQLResult{Status: "pending", RunID: validation.RunID}, errCodeQLPending
	}
	return codeQLResult{Status: "converged", RunID: validation.RunID, Observed: after.codeQLRouting}, nil
}

func (client codeQLClient) validation(ctx context.Context, runID int64) error {
	for {
		body, status, err := client.request(ctx, http.MethodGet,
			"/repos/"+codeQLRepository+"/actions/runs/"+strconv.FormatInt(runID, 10), nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GitHub CodeQL validation GET returned HTTP %d", status)
		}
		var run struct {
			ID         int64  `json:"id"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		}
		if err := json.Unmarshal(body, &run); err != nil || run.ID != runID {
			return errors.New("invalid CodeQL validation run identity")
		}
		if run.Status == "completed" {
			if run.Conclusion != "success" {
				return errors.New("CodeQL validation run did not succeed")
			}
			return nil
		}
		if err := client.wait(ctx); err != nil {
			return errCodeQLPending
		}
	}
}

func reconcileCodeQL(ctx context.Context, cfg config, rootDir string) int {
	desired, err := loadCodeQLDesired(filepath.Join(rootDir, "codeql-runner.json"))
	if err != nil {
		slog.Error("CodeQL declaration", "err", err)
		return 1
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		slog.Error("CodeQL requires GITHUB_TOKEN")
		return 1
	}
	client := codeQLClient{
		baseURL: "https://api.github.com", token: token,
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		wait: func(ctx context.Context) error {
			timer := time.NewTimer(15 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	result, err := client.reconcile(ctx, desired, cfg.mode)
	if err != nil {
		slog.Error("CodeQL routing not verified", "root", cfg.rootName, "status", result.Status, "validation_run_id", result.RunID, "err", err)
		return 1
	}
	slog.Info("CodeQL routing observation", "root", cfg.rootName, "status", result.Status, "mode", cfg.mode,
		"desired_routing", desired.DefaultSetup, "observed_routing", result.Observed, "validation_run_id", result.RunID)
	return 0
}
