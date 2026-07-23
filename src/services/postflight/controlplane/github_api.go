package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	githubAPIVersion = "2022-11-28"
	githubUserAgent  = "postflight-runner-controlplane"
	// Installation tokens live 1h; refresh at 45m so a token is never
	// presented near expiry.
	installationTokenRefreshAge = 45 * time.Minute
	// Rate-limit waits are honored only up to this bound; anything longer
	// fails the call and falls back to the delivery ledger's retry backoff.
	rateLimitWaitMax = 30 * time.Second
	apiPageSize      = 100
	apiMaxPages      = 10
)

type installationToken struct {
	value    string
	mintedAt time.Time
}

// githubClient is the GitHub App API client: app JWT (RS256 via stdlib
// crypto, no JWT dependency), a token cache keyed by installation, and
// rate-limit awareness on every call.
type githubClient struct {
	baseURL    string
	appID      int64
	key        *rsa.PrivateKey
	httpClient *http.Client
	now        func() time.Time

	mu     sync.Mutex
	tokens map[int64]installationToken
}

func newGitHubClient(cfg config) (*githubClient, error) {
	key, err := parseRSAPrivateKey(cfg.privateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &githubClient{
		baseURL:    strings.TrimRight(cfg.apiBaseURL, "/"),
		appID:      cfg.appID,
		key:        key,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		now:        time.Now,
		tokens:     make(map[int64]installationToken),
	}, nil
}

func parseRSAPrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("GITHUB_APP_PRIVATE_KEY_PEM: no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_PEM: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GITHUB_APP_PRIVATE_KEY_PEM: not an RSA key")
	}
	return key, nil
}

// appJWT hand-rolls the RS256 app JWT. iat is backdated 60s for clock skew;
// exp stays inside GitHub's 10-minute cap.
func (c *githubClient) appJWT(now time.Time) (string, error) {
	b64 := base64.RawURLEncoding.EncodeToString
	header := b64([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := b64([]byte(fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":"%d"}`,
		now.Add(-time.Minute).Unix(), now.Add(9*time.Minute).Unix(), c.appID)))
	signing := header + "." + claims
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

// installationToken returns the cached installation token, minting a fresh
// one via POST /app/installations/{id}/access_tokens once the cache is 45m
// old. The mutex intentionally serializes concurrent mints.
func (c *githubClient) installationAccessToken(ctx context.Context, installationID int64) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if installationID <= 0 {
		return "", errors.New("GitHub installation id must be positive")
	}
	if cached := c.tokens[installationID]; cached.value != "" && c.now().Sub(cached.mintedAt) < installationTokenRefreshAge {
		return cached.value, nil
	}
	for id, cached := range c.tokens {
		if c.now().Sub(cached.mintedAt) >= time.Hour {
			delete(c.tokens, id)
		}
	}
	jwt, err := c.appJWT(c.now())
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", githubUserAgent)
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("mint installation token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("mint installation token: github responded %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("mint installation token: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("mint installation token: empty token in response")
	}
	c.tokens[installationID] = installationToken{value: out.Token, mintedAt: c.now()}
	return out.Token, nil
}

func (c *githubClient) invalidateToken(installationID int64) {
	c.mu.Lock()
	delete(c.tokens, installationID)
	c.mu.Unlock()
}

// doJSON performs one authenticated API call. A 401 re-mints the
// installation token once, and Retry-After / x-ratelimit-remaining=0 are
// honored once with a bounded sleep — independently, so a remint followed by
// a rate limit still waits instead of erroring. Anything else is the
// caller's problem — for worker paths that means the delivery ledger's
// retry/backoff machinery.
//
// The bounded sleep runs on the caller's goroutine. For the worker that is
// THE single sweeper goroutine, so a rate-limit wait stalls all delivery
// processing for up to rateLimitWaitMax; waits longer than that fail fast
// and fall back to the ledger's backoff instead.
func (c *githubClient) doJSON(ctx context.Context, installationID int64, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	reminted, rateWaited := false, false
	for {
		token, err := c.installationAccessToken(ctx, installationID)
		if err != nil {
			return err
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
		req.Header.Set("User-Agent", githubUserAgent)
		req.Header.Set("Authorization", "Bearer "+token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("github %s %s: %w", method, path, err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer resp.Body.Close()
			if out == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				return nil
			}
			if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
				return fmt.Errorf("github %s %s: decode: %w", method, path, err)
			}
			return nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && !reminted {
			reminted = true
			c.invalidateToken(installationID)
			continue
		}
		if wait, ok := rateLimitWait(resp, c.now()); ok && !rateWaited {
			rateWaited = true
			if wait > rateLimitWaitMax {
				return fmt.Errorf("github %s %s: rate limited for %s", method, path, wait)
			}
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("github %s %s: responded %d", method, path, resp.StatusCode)
	}
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

type apiWorkflowRun struct {
	ID           int64  `json:"id"`
	Event        string `json:"event"`
	Path         string `json:"path"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	RunAttempt   int64  `json:"run_attempt"`
	PullRequests []struct {
		Number int64 `json:"number"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_requests"`
	HeadRepository struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type apiWorkflowJob struct {
	ID           int64     `json:"id"`
	RunID        int64     `json:"run_id"`
	RunAttempt   int64     `json:"run_attempt"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	Labels       []string  `json:"labels"`
	RunnerID     int64     `json:"runner_id"`
	RunnerName   string    `json:"runner_name"`
	CheckRunURL  string    `json:"check_run_url"`
	HeadSHA      string    `json:"head_sha"`
	HeadBranch   string    `json:"head_branch"`
	WorkflowName string    `json:"workflow_name"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at"`
}

type apiPullRef struct {
	Number int64  `json:"number"`
	State  string `json:"state"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (c *githubClient) workflowRun(ctx context.Context, installationID int64, repo string, runID int64) (apiWorkflowRun, error) {
	var run apiWorkflowRun
	err := c.doJSON(ctx, installationID, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, runID), nil, &run)
	return run, err
}

// workflowRunAttemptJobs lists the jobs of the EXACT run attempt (terminal
// evidence is attempt-scoped; retried runs must not be conflated).
func (c *githubClient) workflowRunAttemptJobs(ctx context.Context, installationID int64, repo string, runID, attempt int64) ([]apiWorkflowJob, error) {
	var jobs []apiWorkflowJob
	for page := 1; page <= apiMaxPages; page++ {
		var out struct {
			TotalCount int              `json:"total_count"`
			Jobs       []apiWorkflowJob `json:"jobs"`
		}
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=%d&page=%d",
			repo, runID, attempt, apiPageSize, page)
		if err := c.doJSON(ctx, installationID, http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		jobs = append(jobs, out.Jobs...)
		if len(out.Jobs) < apiPageSize || len(jobs) >= out.TotalCount {
			break
		}
	}
	return jobs, nil
}

// generateJITConfig mints a single-use, pre-registered runner for one job:
// POST /orgs/{org}/actions/runners/generate-jitconfig. The returned blob is
// everything the guest needs to register; it is never persisted anywhere
// but the pool member it was minted for.
func (c *githubClient) generateJITConfig(ctx context.Context, installationID int64, org, name string, runnerGroupID int64, labels []string) (string, error) {
	var out struct {
		EncodedJITConfig string `json:"encoded_jit_config"`
	}
	err := c.doJSON(ctx, installationID, http.MethodPost, fmt.Sprintf("/orgs/%s/actions/runners/generate-jitconfig", org),
		map[string]any{"name": name, "runner_group_id": runnerGroupID, "labels": labels}, &out)
	if err != nil {
		return "", err
	}
	if out.EncodedJITConfig == "" {
		return "", errors.New("generate-jitconfig: empty encoded_jit_config in response")
	}
	return out.EncodedJITConfig, nil
}

func (c *githubClient) pullRequestsForCommit(ctx context.Context, installationID int64, repo, sha string) ([]apiPullRef, error) {
	var pulls []apiPullRef
	err := c.doJSON(ctx, installationID, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/pulls", repo, sha), nil, &pulls)
	return pulls, err
}

func (c *githubClient) createIssueComment(ctx context.Context, installationID int64, repo string, issueNumber int64, body string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.doJSON(ctx, installationID, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber),
		map[string]string{"body": body}, &out)
	return out.ID, err
}

func (c *githubClient) updateIssueComment(ctx context.Context, installationID int64, repo string, commentID int64, body string) error {
	return c.doJSON(ctx, installationID, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID),
		map[string]string{"body": body}, nil)
}

// findMarkerComment returns the id of the first bot-authored comment on the
// issue whose body starts with the marker, or 0. Recovery path for a lost
// comment id (create succeeded, the posted-state write failed): adopt instead
// of creating a duplicate. Bot-authored + marker-prefix so a human comment
// merely quoting the marker is never adopted (and PATCHed over).
func (c *githubClient) findMarkerComment(ctx context.Context, installationID int64, repo string, issueNumber int64, marker string) (int64, error) {
	for page := 1; page <= apiMaxPages; page++ {
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				Type string `json:"type"`
			} `json:"user"`
		}
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d&page=%d",
			repo, issueNumber, apiPageSize, page)
		if err := c.doJSON(ctx, installationID, http.MethodGet, path, nil, &comments); err != nil {
			return 0, err
		}
		for _, cm := range comments {
			if cm.User.Type == "Bot" && strings.HasPrefix(cm.Body, marker) {
				return cm.ID, nil
			}
		}
		if len(comments) < apiPageSize {
			return 0, nil
		}
	}
	return 0, nil
}
