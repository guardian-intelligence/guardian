package checkoutbundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The handler tests run the full pipeline against a real git upstream served
// over file://, mirroring the action's ten contract cases from the server
// side. They skip when git is unavailable; CI runners and dev machines have
// it.

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// makeUpstream creates <root>/acme/widget.git with one commit on main and
// returns (root, commit sha).
func makeUpstream(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	work := t.TempDir()
	testGit(t, work, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("postflight\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "add", ".")
	testGit(t, work, "commit", "-m", "initial")
	sha := testGit(t, work, "rev-parse", "HEAD")
	upstream := filepath.Join(root, "acme", "widget.git")
	if err := os.MkdirAll(filepath.Dir(upstream), 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "clone", "--bare", ".", upstream)
	// GitHub allows fetching commits by SHA (allowReachableSHA1InWant); stock
	// file-transport upload-pack does not, so grant it explicitly to keep the
	// sha-fallback path testable.
	testGit(t, upstream, "config", "uploadpack.allowAnySHA1InWant", "true")
	return root, sha
}

const (
	testExecution = "11111111-1111-4111-8111-111111111111"
	testAttempt   = "22222222-2222-4222-8222-222222222222"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func newTestService(t *testing.T, upstreamRoot string, mutate func(*Config)) *Service {
	t.Helper()
	cfg := Config{
		StoreDir:         t.TempDir(),
		HostSecret:       testSecret,
		GitHubWebBaseURL: "file://" + upstreamRoot,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg, &StaticResolver{Leases: []LeaseIdentity{{
		ExecutionID:        testExecution,
		AttemptID:          testAttempt,
		InstallationID:     42,
		RepositoryID:       4242,
		RepositoryFullName: "acme/widget",
	}}})
}

type bundleResponse struct {
	status  int
	headers http.Header
	body    []byte
}

func requestBundle(t *testing.T, service *Service, mutate func(*http.Request), body map[string]string) bundleResponse {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", BundlePath, bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(executionIDHeader, testExecution)
	r.Header.Set(attemptIDHeader, testAttempt)
	r.Header.Set("Authorization", "Bearer "+DeriveCheckoutToken(testSecret, testExecution, testAttempt))
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	service.Handler().ServeHTTP(w, r)
	response := w.Result()
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return bundleResponse{status: response.StatusCode, headers: response.Header, body: data}
}

func validBody(sha string) map[string]string {
	return map[string]string{
		"repository":   "acme/widget",
		"ref":          "refs/heads/main",
		"sha":          sha,
		"github_token": "file-transport-ignores-this",
	}
}

// assertServedPack checks every success-contract requirement the action
// enforces, then proves the pack is real by materializing it exactly the way
// the action does: git init + index-pack --stdin + checkout --detach.
func assertServedPack(t *testing.T, response bundleResponse, sha string, wantCacheHit bool) {
	t.Helper()
	if response.status != http.StatusOK {
		t.Fatalf("status %d, body %s", response.status, response.body)
	}
	if got := response.headers.Get("Content-Type"); got != packContentType {
		t.Fatalf("content-type %q", got)
	}
	if got := response.headers.Get(shaHeader); got != sha {
		t.Fatalf("%s = %q, want %q", shaHeader, got, sha)
	}
	declared, err := strconv.Atoi(response.headers.Get(sizeHeader))
	if err != nil || declared != len(response.body) {
		t.Fatalf("%s = %q, body is %d bytes", sizeHeader, response.headers.Get(sizeHeader), len(response.body))
	}
	if got := response.headers.Get("Content-Length"); got != strconv.Itoa(len(response.body)) {
		t.Fatalf("content-length %q, body is %d bytes", got, len(response.body))
	}
	if got := response.headers.Get(cacheHitHeader); got != strconv.FormatBool(wantCacheHit) {
		t.Fatalf("%s = %q, want %v", cacheHitHeader, got, wantCacheHit)
	}

	target := t.TempDir()
	testGit(t, target, "init", ".")
	packPath := filepath.Join(t.TempDir(), "response.pack")
	if err := os.WriteFile(packPath, response.body, 0o600); err != nil {
		t.Fatal(err)
	}
	indexPack := exec.Command("git", "index-pack", "--stdin")
	indexPack.Dir = target
	packFile, err := os.Open(packPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = packFile.Close() }()
	indexPack.Stdin = packFile
	if output, err := indexPack.CombinedOutput(); err != nil {
		t.Fatalf("index-pack rejected the served bytes: %v: %s", err, output)
	}
	testGit(t, target, "checkout", "--force", "--detach", sha)
	if head := testGit(t, target, "rev-parse", "HEAD"); head != sha {
		t.Fatalf("materialized HEAD %s, want %s", head, sha)
	}
	content, err := os.ReadFile(filepath.Join(target, "hello.txt"))
	if err != nil || string(content) != "postflight\n" {
		t.Fatalf("worktree content wrong: %q, %v", content, err)
	}
}

func TestBundleHappyPathThenCacheHit(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)

	first := requestBundle(t, service, nil, validBody(sha))
	assertServedPack(t, first, sha, false)

	// Remove the upstream entirely: the cache hit must not need it.
	if err := os.RemoveAll(upstreamRoot); err != nil {
		t.Fatal(err)
	}
	second := requestBundle(t, service, nil, validBody(sha))
	assertServedPack(t, second, sha, true)
	if !bytes.Equal(first.body, second.body) {
		t.Fatal("cache served different bytes")
	}
	if hits := service.Metrics.CacheHits.Load(); hits != 1 {
		t.Fatalf("cache hits = %d, want 1", hits)
	}
}

func TestBundleShaOnlyRequest(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)
	body := validBody(sha)
	body["ref"] = ""
	response := requestBundle(t, service, nil, body)
	assertServedPack(t, response, sha, false)
}

func TestBundlePullRequestRef(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	// Simulate GitHub's refs/pull/1/merge advertising the merge commit.
	testGit(t, filepath.Join(upstreamRoot, "acme", "widget.git"),
		"update-ref", "refs/pull/1/merge", sha)
	service := newTestService(t, upstreamRoot, nil)
	body := validBody(sha)
	body["ref"] = "refs/pull/1/merge"
	response := requestBundle(t, service, nil, body)
	assertServedPack(t, response, sha, false)
}

func TestBundleAuthenticationFailures(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)

	cases := map[string]func(*http.Request){
		"missing bearer":    func(r *http.Request) { r.Header.Del("Authorization") },
		"tampered bearer":   func(r *http.Request) { r.Header.Set("Authorization", "Bearer forged-token-value") },
		"missing execution": func(r *http.Request) { r.Header.Del(executionIDHeader) },
		"missing attempt":   func(r *http.Request) { r.Header.Del(attemptIDHeader) },
		"unknown lease": func(r *http.Request) {
			r.Header.Set(executionIDHeader, "no-such-execution")
			r.Header.Set("Authorization", "Bearer "+DeriveCheckoutToken(testSecret, "no-such-execution", testAttempt))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			response := requestBundle(t, service, mutate, validBody(sha))
			if response.status != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", response.status)
			}
			assertJSONError(t, response)
		})
	}
}

func TestBundleResolverUnavailableIsRetryable(t *testing.T) {
	service := New(Config{StoreDir: t.TempDir(), HostSecret: testSecret}, erroringResolver{})
	payload, _ := json.Marshal(validBody(strings.Repeat("a", 40)))
	r := httptest.NewRequest("POST", BundlePath, bytes.NewReader(payload))
	r.Header.Set(executionIDHeader, testExecution)
	r.Header.Set(attemptIDHeader, testAttempt)
	r.Header.Set("Authorization", "Bearer "+DeriveCheckoutToken(testSecret, testExecution, testAttempt))
	w := httptest.NewRecorder()
	service.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After missing on 503")
	}
}

func TestBundleRepositoryOutsideLease(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)
	body := validBody(sha)
	body["repository"] = "acme/other"
	response := requestBundle(t, service, nil, body)
	if response.status != http.StatusForbidden {
		t.Fatalf("status %d, want 403", response.status)
	}
	assertJSONError(t, response)
}

func TestBundleInvalidRequests(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)

	cases := map[string]map[string]string{
		"bad sha":       func() map[string]string { b := validBody(sha); b["sha"] = "notasha"; return b }(),
		"partial ref":   func() map[string]string { b := validBody(sha); b["ref"] = "main"; return b }(),
		"missing token": func() map[string]string { b := validBody(sha); delete(b, "github_token"); return b }(),
		"bad repo":      func() map[string]string { b := validBody(sha); b["repository"] = "acme"; return b }(),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			response := requestBundle(t, service, nil, body)
			if response.status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", response.status, response.body)
			}
			assertJSONError(t, response)
		})
	}

	t.Run("malformed json", func(t *testing.T) {
		r := httptest.NewRequest("POST", BundlePath, strings.NewReader("{not json"))
		r.Header.Set(executionIDHeader, testExecution)
		r.Header.Set(attemptIDHeader, testAttempt)
		r.Header.Set("Authorization", "Bearer "+DeriveCheckoutToken(testSecret, testExecution, testAttempt))
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", w.Code)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		r := httptest.NewRequest("GET", BundlePath, nil)
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status %d, want 405", w.Code)
		}
	})
}

func TestBundleUnknownShaIsNotFound(t *testing.T) {
	requireGit(t)
	upstreamRoot, _ := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)
	body := validBody(strings.Repeat("d", 40))
	body["ref"] = "" // force the sha-fetch path
	response := requestBundle(t, service, nil, body)
	if response.status != http.StatusNotFound {
		t.Fatalf("status %d, want 404 (body %s)", response.status, response.body)
	}
	assertJSONError(t, response)
}

func TestBundleMissingRefFallsBackToSha(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)
	// The named branch does not exist; the advertised sha still resolves.
	body := validBody(sha)
	body["ref"] = "refs/heads/deleted-branch"
	response := requestBundle(t, service, nil, body)
	assertServedPack(t, response, sha, false)
}

func TestBundleTooLarge(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, func(cfg *Config) { cfg.MaxPackBytes = 16 })
	response := requestBundle(t, service, nil, validBody(sha))
	if response.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413 (body %s)", response.status, response.body)
	}
	assertJSONError(t, response)
	// The failed pack must not have been published to the cache.
	follow := requestBundle(t, newTestService(t, upstreamRoot, nil), nil, validBody(sha))
	assertServedPack(t, follow, sha, false)
}

func TestBundleThrottled(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, func(cfg *Config) { cfg.MaxConcurrent = 1 })
	service.slots <- struct{}{} // occupy the only slot
	response := requestBundle(t, service, nil, validBody(sha))
	if response.status != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", response.status)
	}
	if response.headers.Get("Retry-After") == "" {
		t.Fatal("Retry-After missing")
	}
	<-service.slots
	recovered := requestBundle(t, service, nil, validBody(sha))
	assertServedPack(t, recovered, sha, false)
}

func TestBundleConcurrentSameShaFetchesOnce(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)

	const clients = 4
	responses := make([]bundleResponse, clients)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, _ := json.Marshal(validBody(sha))
			r := httptest.NewRequest("POST", BundlePath, bytes.NewReader(payload))
			r.Header.Set(executionIDHeader, testExecution)
			r.Header.Set(attemptIDHeader, testAttempt)
			r.Header.Set("Authorization", "Bearer "+DeriveCheckoutToken(testSecret, testExecution, testAttempt))
			w := httptest.NewRecorder()
			service.Handler().ServeHTTP(w, r)
			result := w.Result()
			body, _ := io.ReadAll(result.Body)
			_ = result.Body.Close()
			responses[i] = bundleResponse{status: result.StatusCode, headers: result.Header, body: body}
		}()
	}
	wg.Wait()
	for i, response := range responses {
		if response.status != http.StatusOK {
			t.Fatalf("client %d got %d: %s", i, response.status, response.body)
		}
	}
	if fetches := service.Metrics.MirrorFetches.Load(); fetches != 1 {
		t.Fatalf("mirror fetches = %d, want 1 (repo lock must collapse concurrent misses)", fetches)
	}
	if hits := service.Metrics.CacheHits.Load(); hits != clients-1 {
		t.Fatalf("cache hits = %d, want %d", hits, clients-1)
	}
}

func TestBundleResponseNeverEchoesToken(t *testing.T) {
	requireGit(t)
	upstreamRoot, sha := makeUpstream(t)
	service := newTestService(t, upstreamRoot, nil)
	secretValue := "ghs_supersecret_1234567890"
	body := validBody(sha)
	body["github_token"] = secretValue
	response := requestBundle(t, service, nil, body)
	if response.status != http.StatusOK {
		t.Fatalf("status %d", response.status)
	}
	for header, values := range response.headers {
		for _, value := range values {
			if strings.Contains(value, secretValue) {
				t.Fatalf("token leaked in header %s", header)
			}
		}
	}
	// A wrong-sha failure must not echo it either.
	failure := requestBundle(t, service, nil, func() map[string]string {
		b := validBody(strings.Repeat("e", 40))
		b["ref"] = ""
		b["github_token"] = secretValue
		return b
	}())
	if strings.Contains(string(failure.body), secretValue) {
		t.Fatal("token leaked in error body")
	}
}

func assertJSONError(t *testing.T, response bundleResponse) {
	t.Helper()
	if got := response.headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("error content-type %q", got)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.body, &payload); err != nil || payload["error"] == "" {
		t.Fatalf("error body not {\"error\": ...}: %s", response.body)
	}
}

func TestClassifyFetchError(t *testing.T) {
	notFound := []string{
		"git fetch_sha: exit status 128: fatal: remote error: upload-pack: not our ref dddd",
		"git fetch_ref: exit status 128: fatal: couldn't find remote ref refs/heads/gone",
		"git fetch_sha: exit status 128: remote: Repository not found.",
		"git fetch_sha: exit status 128: fatal: Authentication failed for 'https://github.com/a/b.git/'",
		"git fetch_sha: exit status 128: error: Server does not allow request for unadvertised object",
	}
	for _, message := range notFound {
		if err := classifyFetchError(fmt.Errorf("%s", message)); !strings.Contains(err.Error(), errNotFound.Error()) {
			t.Fatalf("%q classified as %v, want not-found", message, err)
		}
	}
	transient := []string{
		"git fetch_sha: exit status 128: fatal: unable to access 'https://github.com/a/b.git/': Could not resolve host",
		"git fetch_sha: signal: killed",
	}
	for _, message := range transient {
		if err := classifyFetchError(fmt.Errorf("%s", message)); !strings.Contains(err.Error(), errUpstream.Error()) {
			t.Fatalf("%q classified as %v, want upstream", message, err)
		}
	}
}
