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
	githubUserAgent  = "verself-runner-controlplane"
	// Installation tokens live 1h; refresh at 45m so a token is never
	// presented near expiry.
	installationTokenRefreshAge = 45 * time.Minute
	// Rate-limit waits are honored only up to this bound; anything longer
	// fails the call and falls back to the delivery ledger's retry backoff.
	rateLimitWaitMax = 30 * time.Second
	apiPageSize      = 100
	apiMaxPages      = 10
)

// githubClient is the GitHub App API client: app JWT (RS256 via stdlib
// crypto, no JWT dependency), one cached installation token, and rate-limit
// awareness on every call.
type githubClient struct {
	baseURL        string
	appID          int64
	installationID int64
	key            *rsa.PrivateKey
	httpClient     *http.Client
	now            func() time.Time

	mu            sync.Mutex
	token         string
	tokenMintedAt time.Time
}

func newGitHubClient(cfg config) (*githubClient, error) {
	key, err := parseRSAPrivateKey(cfg.privateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &githubClient{
		baseURL:        strings.TrimRight(cfg.apiBaseURL, "/"),
		appID:          cfg.appID,
		installationID: cfg.installationID,
		key:            key,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		now:            time.Now,
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
func (c *githubClient) installationToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Sub(c.tokenMintedAt) < installationTokenRefreshAge {
		return c.token, nil
	}
	jwt, err := c.appJWT(c.now())
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, c.installationID)
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
	c.token = out.Token
	c.tokenMintedAt = c.now()
	return c.token, nil
}

func (c *githubClient) invalidateToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// doJSON performs one authenticated API call. Retry-After and
// x-ratelimit-remaining=0 are honored with a bounded sleep and a single
// retry; a 401 re-mints the installation token once. Anything else is the
// caller's problem — for worker paths that means the delivery ledger's
// retry/backoff machinery.
func (c *githubClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		token, err := c.installationToken(ctx)
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
		if attempt == 0 {
			if resp.StatusCode == http.StatusUnauthorized {
				c.invalidateToken()
				continue
			}
			if wait, ok := rateLimitWait(resp, c.now()); ok {
				if wait > rateLimitWaitMax {
					return fmt.Errorf("github %s %s: rate limited for %s", method, path, wait)
				}
				if err := sleepCtx(ctx, wait); err != nil {
					return err
				}
				continue
			}
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
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	RunAttempt   int64  `json:"run_attempt"`
	PullRequests []struct {
		Number int64 `json:"number"`
	} `json:"pull_requests"`
	HeadRepository struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
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
}

func (c *githubClient) workflowRun(ctx context.Context, repo string, runID int64) (apiWorkflowRun, error) {
	var run apiWorkflowRun
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, runID), nil, &run)
	return run, err
}

// workflowRunAttemptJobs lists the jobs of the EXACT run attempt (terminal
// evidence is attempt-scoped; retried runs must not be conflated).
func (c *githubClient) workflowRunAttemptJobs(ctx context.Context, repo string, runID, attempt int64) ([]apiWorkflowJob, error) {
	var jobs []apiWorkflowJob
	for page := 1; page <= apiMaxPages; page++ {
		var out struct {
			TotalCount int              `json:"total_count"`
			Jobs       []apiWorkflowJob `json:"jobs"`
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

func (c *githubClient) pullRequestsForCommit(ctx context.Context, repo, sha string) ([]apiPullRef, error) {
	var pulls []apiPullRef
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/pulls", repo, sha), nil, &pulls)
	return pulls, err
}

func (c *githubClient) createIssueComment(ctx context.Context, repo string, issueNumber int64, body string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber),
		map[string]string{"body": body}, &out)
	return out.ID, err
}

func (c *githubClient) updateIssueComment(ctx context.Context, repo string, commentID int64, body string) error {
	return c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID),
		map[string]string{"body": body}, nil)
}
