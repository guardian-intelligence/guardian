package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIVersion = "2022-11-28"
	githubUserAgent  = "postflight-hammer"
	apiPageSize      = 100
	apiMaxPages      = 10
	rateLimitWaitMax = 60 * time.Second
	// maxLogArchiveBytes bounds one attempt's log zip in memory.
	maxLogArchiveBytes = 256 << 20
)

// ghClient is a token-authenticated GitHub REST client. Unlike the control
// plane it never mints App tokens: the hammer runs as an operator, on
// GITHUB_TOKEN or the ambient `gh auth token`.
type ghClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	now        func() time.Time
}

func newGHClient(baseURL, token string) (*ghClient, error) {
	if token == "" {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			return nil, errors.New("no GitHub credential: set GITHUB_TOKEN or log in with `gh auth login`")
		}
		token = strings.TrimSpace(string(out))
	}
	if token == "" {
		return nil, errors.New("no GitHub credential: set GITHUB_TOKEN or log in with `gh auth login`")
	}
	return &ghClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		now:        time.Now,
	}, nil
}

func (c *ghClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	rateWaited := false
	for {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
		req.Header.Set("User-Agent", githubUserAgent)
		req.Header.Set("Authorization", "Bearer "+c.token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github %s %s: %w", method, path, err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if wait, ok := rateLimitWait(resp, c.now()); ok && !rateWaited {
			rateWaited = true
			if wait > rateLimitWaitMax {
				return nil, fmt.Errorf("github %s %s: rate limited for %s", method, path, wait)
			}
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}
		return nil, &ghStatusError{method: method, path: path, status: resp.StatusCode}
	}
}

// ghStatusError is a non-2xx API response, so callers can branch on the
// status (e.g. a cancel losing the race to completion answers 409).
type ghStatusError struct {
	method string
	path   string
	status int
}

func (e *ghStatusError) Error() string {
	return fmt.Sprintf("github %s %s: responded %d", e.method, e.path, e.status)
}

func (c *ghClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("github %s %s: decode: %w", method, path, err)
	}
	return nil
}

func rateLimitWait(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
	}
	if resp.Header.Get("x-ratelimit-remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("x-ratelimit-reset"), 10, 64); err == nil {
			wait := time.Unix(reset, 0).Sub(now)
			if wait < 0 {
				wait = 0
			}
			return wait, true
		}
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type ghRun struct {
	ID           int64     `json:"id"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	RunAttempt   int64     `json:"run_attempt"`
	CreatedAt    time.Time `json:"created_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	Path         string    `json:"path"`
	Name         string    `json:"name"`
}

type ghJob struct {
	ID          int64     `json:"id"`
	RunID       int64     `json:"run_id"`
	RunAttempt  int64     `json:"run_attempt"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	RunnerName  string    `json:"runner_name"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Steps       []ghStep  `json:"steps"`
}

type ghStep struct {
	Name        string     `json:"name"`
	Number      int64      `json:"number"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

func (c *ghClient) dispatchWorkflow(ctx context.Context, repo, workflow, ref string, inputs map[string]string) error {
	body := map[string]any{"ref": ref}
	if len(inputs) > 0 {
		body["inputs"] = inputs
	}
	return c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/actions/workflows/%s/dispatches", repo, url.PathEscape(workflow)),
		body, nil)
}

// listWorkflowRuns lists the workflow's dispatch-triggered runs created at or
// after since (callers pass a since already backed off past the battery
// start, so boundary truncation cannot hide the first run).
func (c *ghClient) listWorkflowRuns(ctx context.Context, repo, workflow string, since time.Time) ([]ghRun, error) {
	var runs []ghRun
	for page := 1; page <= apiMaxPages; page++ {
		q := url.Values{}
		q.Set("event", "workflow_dispatch")
		q.Set("created", ">="+since.UTC().Format(time.RFC3339))
		q.Set("per_page", strconv.Itoa(apiPageSize))
		q.Set("page", strconv.Itoa(page))
		var out struct {
			TotalCount int     `json:"total_count"`
			Runs       []ghRun `json:"workflow_runs"`
		}
		path := fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?%s", repo, url.PathEscape(workflow), q.Encode())
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		runs = append(runs, out.Runs...)
		if len(out.Runs) < apiPageSize || len(runs) >= out.TotalCount {
			break
		}
	}
	return runs, nil
}

func (c *ghClient) getRun(ctx context.Context, repo string, runID int64) (ghRun, error) {
	var run ghRun
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, runID), nil, &run)
	return run, err
}

func (c *ghClient) runAttempt(ctx context.Context, repo string, runID, attempt int64) (ghRun, error) {
	var run ghRun
	err := c.doJSON(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d", repo, runID, attempt), nil, &run)
	return run, err
}

func (c *ghClient) attemptJobs(ctx context.Context, repo string, runID, attempt int64) ([]ghJob, error) {
	var jobs []ghJob
	for page := 1; page <= apiMaxPages; page++ {
		var out struct {
			TotalCount int     `json:"total_count"`
			Jobs       []ghJob `json:"jobs"`
		}
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=%d&page=%d",
			repo, runID, attempt, apiPageSize, page)
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		jobs = append(jobs, out.Jobs...)
		if len(out.Jobs) < apiPageSize || len(jobs) >= out.TotalCount {
			break
		}
	}
	return jobs, nil
}

func (c *ghClient) cancelRun(ctx context.Context, repo string, runID int64) error {
	return c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/actions/runs/%d/cancel", repo, runID), nil, nil)
}

func (c *ghClient) rerunRun(ctx context.Context, repo string, runID int64) error {
	return c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/actions/runs/%d/rerun", repo, runID), nil, nil)
}

// attemptLogs downloads the attempt's log archive and returns each entry's
// path mapped to its uncompressed size — the machine-readable equivalent of
// `gh run view --log`, sized so the report can assert every step's log is
// non-empty without shipping the log text around.
func (c *ghClient) attemptLogs(ctx context.Context, repo string, runID, attempt int64) (map[string]int64, error) {
	resp, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/logs", repo, runID, attempt), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLogArchiveBytes))
	if err != nil {
		return nil, fmt.Errorf("log archive for run %d attempt %d: %w", runID, attempt, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("log archive for run %d attempt %d: %w", runID, attempt, err)
	}
	sizes := map[string]int64{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		sizes[f.Name] = int64(f.UncompressedSize64)
	}
	return sizes, nil
}
