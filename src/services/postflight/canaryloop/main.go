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
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/crypto/x509roots/fallback"
)

const (
	githubAPIVersion = "2022-11-28"
	githubUserAgent  = "postflight-canary-loop"
	branchPrefix     = "postflight-canary/"
	maxPullAge       = time.Hour
)

type config struct {
	apiBaseURL       string
	appID            int64
	installationID   int64
	privateKeyPEM    string
	owner            string
	repository       string
	baseBranch       string
	targetCommit     string
	targetPath       string
	upstreamPR       string
	expectedCheck    string
	metricsInsertURL string
}

type statusError struct {
	method string
	path   string
	code   int
}

func (e statusError) Error() string {
	return fmt.Sprintf("github %s %s responded %d", e.method, e.path, e.code)
}

type githubClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type runner struct {
	cfg config
	gh  *githubClient
	now func() time.Time
}

type pullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Head      struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	key, err := parseRSAPrivateKey(cfg.privateKeyPEM)
	if err != nil {
		slog.Error("private key", "err", err)
		os.Exit(1)
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	token, err := mintInstallationToken(ctx, httpClient, cfg.apiBaseURL, cfg.appID, cfg.installationID, key, time.Now())
	if err != nil {
		pushMetrics(context.Background(), httpClient, cfg.metricsInsertURL, "failed", false)
		slog.Error("github authentication", "err", err)
		os.Exit(1)
	}

	r := &runner{
		cfg: cfg,
		gh: &githubClient{
			baseURL:    strings.TrimRight(cfg.apiBaseURL, "/"),
			token:      token,
			httpClient: httpClient,
		},
		now: time.Now,
	}
	outcome, err := r.run(ctx)
	if err != nil {
		pushMetrics(context.Background(), httpClient, cfg.metricsInsertURL, "failed", false)
		slog.Error("canary loop", "err", err)
		os.Exit(1)
	}
	pushMetrics(context.Background(), httpClient, cfg.metricsInsertURL, outcome, true)
	slog.Info("canary loop complete", "outcome", outcome)
}

func loadConfig() (config, error) {
	cfg := config{
		apiBaseURL:       valueOrDefault("GITHUB_API_URL", "https://api.github.com"),
		privateKeyPEM:    os.Getenv("GITHUB_APP_PRIVATE_KEY_PEM"),
		owner:            os.Getenv("GITHUB_OWNER"),
		repository:       os.Getenv("GITHUB_REPOSITORY"),
		baseBranch:       valueOrDefault("GITHUB_BASE_BRANCH", "main"),
		targetCommit:     os.Getenv("TARGET_MERGE_COMMIT"),
		targetPath:       os.Getenv("TARGET_PATH"),
		upstreamPR:       os.Getenv("UPSTREAM_PR"),
		expectedCheck:    os.Getenv("EXPECTED_CHECK"),
		metricsInsertURL: os.Getenv("VMINSERT_URL"),
	}
	var err error
	if cfg.appID, err = positiveInt64Env("GITHUB_APP_ID"); err != nil {
		return config{}, err
	}
	if cfg.installationID, err = positiveInt64Env("GITHUB_INSTALLATION_ID"); err != nil {
		return config{}, err
	}
	required := map[string]string{
		"GITHUB_APP_PRIVATE_KEY_PEM": cfg.privateKeyPEM,
		"GITHUB_OWNER":               cfg.owner,
		"GITHUB_REPOSITORY":          cfg.repository,
		"GITHUB_BASE_BRANCH":         cfg.baseBranch,
		"TARGET_MERGE_COMMIT":        cfg.targetCommit,
		"TARGET_PATH":                cfg.targetPath,
		"UPSTREAM_PR":                cfg.upstreamPR,
		"EXPECTED_CHECK":             cfg.expectedCheck,
		"VMINSERT_URL":               cfg.metricsInsertURL,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return config{}, fmt.Errorf("%s is required", name)
		}
	}
	if strings.Contains(cfg.targetPath, "..") || strings.HasPrefix(cfg.targetPath, "/") {
		return config{}, errors.New("TARGET_PATH must be a repository-relative path")
	}
	return cfg, nil
}

func positiveInt64Env(name string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func parseRSAPrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("GITHUB_APP_PRIVATE_KEY_PEM contains no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key is not RSA")
	}
	return key, nil
}

func appJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	b64 := base64.RawURLEncoding.EncodeToString
	header := b64([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := b64([]byte(fmt.Sprintf(
		`{"iat":%d,"exp":%d,"iss":"%d"}`,
		now.Add(-time.Minute).Unix(),
		now.Add(9*time.Minute).Unix(),
		appID,
	)))
	signing := header + "." + claims
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(signature), nil
}

func mintInstallationToken(
	ctx context.Context,
	httpClient *http.Client,
	apiBaseURL string,
	appID, installationID int64,
	key *rsa.PrivateKey,
	now time.Time,
) (string, error) {
	jwt, err := appJWT(appID, key, now)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(apiBaseURL, "/")+path,
		nil,
	)
	if err != nil {
		return "", err
	}
	setGitHubHeaders(req, "Bearer "+jwt)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("mint installation token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", statusError{method: http.MethodPost, path: path, code: resp.StatusCode}
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("decode installation token: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("GitHub returned an empty installation token")
	}
	return out.Token, nil
}

func setGitHubHeaders(req *http.Request, authorization string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", githubUserAgent)
	req.Header.Set("Authorization", authorization)
}

func (c *githubClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	setGitHubHeaders(req, "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return statusError{method: method, path: path, code: resp.StatusCode}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode github %s %s: %w", method, path, err)
	}
	return nil
}

func (r *runner) run(ctx context.Context) (string, error) {
	open, err := r.openCanaryPullRequests(ctx)
	if err != nil {
		return "", err
	}
	switch len(open) {
	case 0:
		return r.createNextPullRequest(ctx)
	case 1:
		return r.reconcilePullRequest(ctx, open[0])
	default:
		return "", fmt.Errorf("found %d open canary pull requests; refusing to choose", len(open))
	}
}

func (r *runner) repositoryPath(suffix string) string {
	return "/repos/" + url.PathEscape(r.cfg.owner) + "/" + url.PathEscape(r.cfg.repository) + suffix
}

func (r *runner) openCanaryPullRequests(ctx context.Context) ([]pullRequest, error) {
	path := r.repositoryPath("/pulls") + "?state=open&base=" + url.QueryEscape(r.cfg.baseBranch) + "&per_page=100"
	var pulls []pullRequest
	if err := r.gh.doJSON(ctx, http.MethodGet, path, nil, &pulls); err != nil {
		return nil, err
	}
	expectedRepo := r.cfg.owner + "/" + r.cfg.repository
	canaries := make([]pullRequest, 0, 1)
	for _, pull := range pulls {
		if strings.HasPrefix(pull.Head.Ref, branchPrefix) && pull.Head.Repo.FullName == expectedRepo {
			canaries = append(canaries, pull)
		}
	}
	return canaries, nil
}

func (r *runner) reconcilePullRequest(ctx context.Context, pull pullRequest) (string, error) {
	path := r.repositoryPath("/commits/") + url.PathEscape(pull.Head.SHA) + "/check-runs"
	var checks struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	if err := r.gh.doJSON(ctx, http.MethodGet, path, nil, &checks); err != nil {
		return "", err
	}
	for _, check := range checks.CheckRuns {
		if check.Name != r.cfg.expectedCheck {
			continue
		}
		if check.Status != "completed" {
			if r.pullExpired(pull) {
				return "", fmt.Errorf(
					"required check %q is still %q on pull request #%d after %s",
					r.cfg.expectedCheck,
					check.Status,
					pull.Number,
					maxPullAge,
				)
			}
			return "waiting", nil
		}
		if check.Conclusion != "success" {
			return "", fmt.Errorf(
				"required check %q concluded %q on pull request #%d",
				r.cfg.expectedCheck,
				check.Conclusion,
				pull.Number,
			)
		}
		var merge struct {
			Merged  bool   `json:"merged"`
			Message string `json:"message"`
		}
		mergePath := r.repositoryPath("/pulls/") + strconv.Itoa(pull.Number) + "/merge"
		if err := r.gh.doJSON(
			ctx,
			http.MethodPut,
			mergePath,
			map[string]string{
				"merge_method": "squash",
				"commit_title": pull.Title,
			},
			&merge,
		); err != nil {
			return "", err
		}
		if !merge.Merged {
			return "", fmt.Errorf("GitHub did not merge pull request #%d: %s", pull.Number, merge.Message)
		}
		deletePath := r.repositoryPath("/git/refs/heads/") + escapeRef(pull.Head.Ref)
		if err := r.gh.doJSON(ctx, http.MethodDelete, deletePath, nil, nil); err != nil {
			return "", fmt.Errorf("delete merged branch %s: %w", pull.Head.Ref, err)
		}
		return "merged", nil
	}
	if r.pullExpired(pull) {
		return "", fmt.Errorf(
			"required check %q did not appear on pull request #%d after %s",
			r.cfg.expectedCheck,
			pull.Number,
			maxPullAge,
		)
	}
	return "waiting", nil
}

func (r *runner) pullExpired(pull pullRequest) bool {
	return !pull.CreatedAt.IsZero() && r.now().Sub(pull.CreatedAt) > maxPullAge
}

func (r *runner) createNextPullRequest(ctx context.Context) (string, error) {
	mainRef, err := r.ref(ctx, r.cfg.baseBranch)
	if err != nil {
		return "", err
	}
	currentCommit, err := r.commit(ctx, mainRef)
	if err != nil {
		return "", err
	}
	targetCommit, err := r.commit(ctx, r.cfg.targetCommit)
	if err != nil {
		return "", err
	}
	if len(targetCommit.Parents) != 1 {
		return "", fmt.Errorf("target commit %s has %d parents; expected one", r.cfg.targetCommit, len(targetCommit.Parents))
	}

	currentBlob, err := r.blobAt(ctx, r.cfg.targetPath, mainRef)
	if err != nil {
		return "", err
	}
	appliedBlob, err := r.blobAt(ctx, r.cfg.targetPath, r.cfg.targetCommit)
	if err != nil {
		return "", err
	}
	revertedBlob, err := r.blobAt(ctx, r.cfg.targetPath, targetCommit.Parents[0].SHA)
	if err != nil {
		return "", err
	}

	direction := ""
	desiredBlob := ""
	switch currentBlob {
	case appliedBlob:
		direction = "revert"
		desiredBlob = revertedBlob
	case revertedBlob:
		direction = "reapply"
		desiredBlob = appliedBlob
	default:
		return "", fmt.Errorf(
			"%s is neither the applied nor reverted upstream blob; refusing to overwrite drift",
			r.cfg.targetPath,
		)
	}

	branch := branchPrefix + direction + "-" + shortSHA(mainRef)
	branchSHA, exists, err := r.refIfExists(ctx, branch)
	if err != nil {
		return "", err
	}
	title := fmt.Sprintf("test(postflight): %s %s", direction, r.cfg.upstreamPR)
	if !exists {
		var tree struct {
			SHA string `json:"sha"`
		}
		if err := r.gh.doJSON(
			ctx,
			http.MethodPost,
			r.repositoryPath("/git/trees"),
			map[string]any{
				"base_tree": currentCommit.Tree.SHA,
				"tree": []map[string]string{{
					"path": r.cfg.targetPath,
					"mode": "100644",
					"type": "blob",
					"sha":  desiredBlob,
				}},
			},
			&tree,
		); err != nil {
			return "", err
		}
		if tree.SHA == "" {
			return "", errors.New("GitHub returned an empty tree SHA")
		}

		var createdCommit struct {
			SHA string `json:"sha"`
		}
		if err := r.gh.doJSON(
			ctx,
			http.MethodPost,
			r.repositoryPath("/git/commits"),
			map[string]any{
				"message": title,
				"tree":    tree.SHA,
				"parents": []string{mainRef},
			},
			&createdCommit,
		); err != nil {
			return "", err
		}
		if createdCommit.SHA == "" {
			return "", errors.New("GitHub returned an empty commit SHA")
		}
		if err := r.gh.doJSON(
			ctx,
			http.MethodPost,
			r.repositoryPath("/git/refs"),
			map[string]string{
				"ref": "refs/heads/" + branch,
				"sha": createdCommit.SHA,
			},
			nil,
		); err != nil {
			return "", err
		}
		branchSHA = createdCommit.SHA
	}

	var pull struct {
		Number int `json:"number"`
	}
	body := fmt.Sprintf(
		"Automated Postflight canary cycle.\n\n"+
			"- Direction: `%s`\n"+
			"- Source: `%s`\n"+
			"- Target merge commit: `%s`\n"+
			"- Generated from base: `%s`\n"+
			"- Head commit: `%s`\n",
		direction,
		r.cfg.upstreamPR,
		r.cfg.targetCommit,
		mainRef,
		branchSHA,
	)
	if err := r.gh.doJSON(
		ctx,
		http.MethodPost,
		r.repositoryPath("/pulls"),
		map[string]string{
			"title": title,
			"head":  branch,
			"base":  r.cfg.baseBranch,
			"body":  body,
		},
		&pull,
	); err != nil {
		return "", err
	}
	if pull.Number <= 0 {
		return "", errors.New("GitHub returned an invalid pull request number")
	}
	return "created", nil
}

func (r *runner) ref(ctx context.Context, branch string) (string, error) {
	sha, exists, err := r.refIfExists(ctx, branch)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("branch %s does not exist", branch)
	}
	return sha, nil
}

func (r *runner) refIfExists(ctx context.Context, branch string) (string, bool, error) {
	path := r.repositoryPath("/git/ref/heads/") + escapeRef(branch)
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := r.gh.doJSON(ctx, http.MethodGet, path, nil, &ref); err != nil {
		var status statusError
		if errors.As(err, &status) && status.code == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	if ref.Object.SHA == "" {
		return "", false, errors.New("GitHub returned an empty ref SHA")
	}
	return ref.Object.SHA, true, nil
}

type gitCommit struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

func (r *runner) commit(ctx context.Context, sha string) (gitCommit, error) {
	path := r.repositoryPath("/git/commits/") + url.PathEscape(sha)
	var commit gitCommit
	if err := r.gh.doJSON(ctx, http.MethodGet, path, nil, &commit); err != nil {
		return gitCommit{}, err
	}
	if commit.SHA == "" || commit.Tree.SHA == "" {
		return gitCommit{}, errors.New("GitHub returned an incomplete commit")
	}
	return commit, nil
}

func (r *runner) blobAt(ctx context.Context, path, ref string) (string, error) {
	escapedPath := strings.Join(pathSegments(path), "/")
	apiPath := r.repositoryPath("/contents/") + escapedPath + "?ref=" + url.QueryEscape(ref)
	var content struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	}
	if err := r.gh.doJSON(ctx, http.MethodGet, apiPath, nil, &content); err != nil {
		return "", err
	}
	if content.Type != "file" || content.SHA == "" {
		return "", fmt.Errorf("%s at %s is not a file", path, ref)
	}
	return content.SHA, nil
}

func pathSegments(path string) []string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return parts
}

func escapeRef(ref string) string {
	return strings.ReplaceAll(url.PathEscape(ref), "%2F", "/")
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func pushMetrics(ctx context.Context, client *http.Client, endpoint, outcome string, success bool) {
	if endpoint == "" {
		return
	}
	value := 0
	if success {
		value = 1
	}
	body := fmt.Sprintf(
		"postflight_canary_loop_heartbeat{outcome=%q} 1\n"+
			"postflight_canary_loop_last_run_success %d\n",
		outcome,
		value,
	)
	metricsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(metricsCtx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		slog.Warn("build metrics request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("push metrics", "err", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("push metrics", "status", resp.StatusCode)
	}
}
