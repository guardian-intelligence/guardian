package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunCreatesRevertPullRequest(t *testing.T) {
	t.Parallel()

	var createdTree map[string]any
	var createdRef map[string]string
	var createdPull map[string]string
	server := newGitHubServer(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls":
			writeJSON(t, w, http.StatusOK, []any{})
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/ref/heads/main":
			writeJSON(t, w, http.StatusOK, map[string]any{"object": map[string]string{"sha": "main000000000000"}})
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/commits/main000000000000":
			writeJSON(t, w, http.StatusOK, testCommit("main000000000000", "current-tree", nil))
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/commits/target":
			writeJSON(t, w, http.StatusOK, testCommit("target", "target-tree", []string{"parent"}))
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/contents/target/file.go":
			switch req.URL.Query().Get("ref") {
			case "main000000000000", "target":
				writeJSON(t, w, http.StatusOK, map[string]string{"type": "file", "sha": "applied-blob"})
			case "parent":
				writeJSON(t, w, http.StatusOK, map[string]string{"type": "file", "sha": "reverted-blob"})
			default:
				t.Fatalf("unexpected contents ref: %s", req.URL.Query().Get("ref"))
			}
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/ref/heads/postflight-canary/revert-main00000000":
			http.Error(w, "not found", http.StatusNotFound)
		case req.Method == http.MethodPost && req.URL.Path == "/repos/test/canary/git/trees":
			decodeJSON(t, req, &createdTree)
			writeJSON(t, w, http.StatusCreated, map[string]string{"sha": "new-tree"})
		case req.Method == http.MethodPost && req.URL.Path == "/repos/test/canary/git/commits":
			writeJSON(t, w, http.StatusCreated, map[string]string{"sha": "new-commit"})
		case req.Method == http.MethodPost && req.URL.Path == "/repos/test/canary/git/refs":
			decodeJSON(t, req, &createdRef)
			writeJSON(t, w, http.StatusCreated, map[string]any{})
		case req.Method == http.MethodPost && req.URL.Path == "/repos/test/canary/pulls":
			decodeJSON(t, req, &createdPull)
			writeJSON(t, w, http.StatusCreated, map[string]int{"number": 17})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
	})
	defer server.Close()

	outcome, err := testRunner(server.URL).run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome != "created" {
		t.Fatalf("outcome = %q, want created", outcome)
	}
	treeEntries := createdTree["tree"].([]any)
	entry := treeEntries[0].(map[string]any)
	if got := entry["sha"]; got != "reverted-blob" {
		t.Fatalf("tree blob = %v, want reverted-blob", got)
	}
	if got := createdRef["ref"]; got != "refs/heads/postflight-canary/revert-main00000000" {
		t.Fatalf("created ref = %q", got)
	}
	if got := createdPull["head"]; got != "postflight-canary/revert-main00000000" {
		t.Fatalf("pull head = %q", got)
	}
}

func TestRunWaitsForPostflightCheck(t *testing.T) {
	t.Parallel()

	server := newGitHubServer(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls":
			writeJSON(t, w, http.StatusOK, []any{testPullRequest(23, "head-sha", time.Now())})
		case serveValidPullValidation(t, w, req):
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/commits/head-sha/check-runs":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"check_runs": []any{
					map[string]string{"name": "targeted-rust-regression", "status": "in_progress", "conclusion": ""},
				},
			})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
	})
	defer server.Close()

	outcome, err := testRunner(server.URL).run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome != "waiting" {
		t.Fatalf("outcome = %q, want waiting", outcome)
	}
}

func TestRunMergesSuccessfulPostflightPullRequest(t *testing.T) {
	t.Parallel()

	var deleted bool
	server := newGitHubServer(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls":
			writeJSON(t, w, http.StatusOK, []any{testPullRequest(23, "head-sha", time.Now())})
		case serveValidPullValidation(t, w, req):
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/commits/head-sha/check-runs":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"check_runs": []any{
					map[string]string{"name": "targeted-rust-regression", "status": "completed", "conclusion": "success"},
				},
			})
		case req.Method == http.MethodPut && req.URL.Path == "/repos/test/canary/pulls/23/merge":
			writeJSON(t, w, http.StatusOK, map[string]any{"merged": true, "message": "Pull Request successfully merged"})
		case req.Method == http.MethodDelete && req.URL.Path == "/repos/test/canary/git/refs/heads/postflight-canary/revert-base":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
	})
	defer server.Close()

	outcome, err := testRunner(server.URL).run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome != "merged" {
		t.Fatalf("outcome = %q, want merged", outcome)
	}
	if !deleted {
		t.Fatal("merged branch was not deleted")
	}
}

func TestRunRefusesFileDrift(t *testing.T) {
	t.Parallel()

	server := newGitHubServer(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls":
			writeJSON(t, w, http.StatusOK, []any{})
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/ref/heads/main":
			writeJSON(t, w, http.StatusOK, map[string]any{"object": map[string]string{"sha": "main"}})
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/commits/main":
			writeJSON(t, w, http.StatusOK, testCommit("main", "current-tree", nil))
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/commits/target":
			writeJSON(t, w, http.StatusOK, testCommit("target", "target-tree", []string{"parent"}))
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/contents/target/file.go":
			sha := map[string]string{
				"main":   "drifted-blob",
				"target": "applied-blob",
				"parent": "reverted-blob",
			}[req.URL.Query().Get("ref")]
			writeJSON(t, w, http.StatusOK, map[string]string{"type": "file", "sha": sha})
		default:
			t.Fatalf("unexpected request after drift detection: %s %s", req.Method, req.URL.String())
		}
	})
	defer server.Close()

	_, err := testRunner(server.URL).run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite drift") {
		t.Fatalf("run error = %v, want drift refusal", err)
	}
}

func TestRunFailsWhenPostflightCheckNeverAppears(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	server := newGitHubServer(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls":
			writeJSON(t, w, http.StatusOK, []any{testPullRequest(23, "head-sha", now.Add(-maxPullAge-time.Minute))})
		case serveValidPullValidation(t, w, req):
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/commits/head-sha/check-runs":
			writeJSON(t, w, http.StatusOK, map[string]any{"check_runs": []any{}})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
	})
	defer server.Close()

	r := testRunner(server.URL)
	r.now = func() time.Time { return now }
	_, err := r.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not appear") {
		t.Fatalf("run error = %v, want missing check failure", err)
	}
}

func TestRunRefusesAdditionalPullRequestFiles(t *testing.T) {
	t.Parallel()

	server := newGitHubServer(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls":
			writeJSON(t, w, http.StatusOK, []any{testPullRequest(23, "head-sha", time.Now())})
		case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls/23/files":
			writeJSON(t, w, http.StatusOK, []any{
				map[string]string{"filename": "target/file.go"},
				map[string]string{"filename": "unexpected"},
			})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
	})
	defer server.Close()

	_, err := testRunner(server.URL).run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "changes 2 files") {
		t.Fatalf("run error = %v, want additional-file refusal", err)
	}
}

func TestPushMetricsLabelsCustomerRepository(t *testing.T) {
	t.Parallel()

	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		payload, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read metrics body: %v", err)
		}
		body = string(payload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	pushMetrics(
		context.Background(),
		server.Client(),
		server.URL,
		"digital-guardian-software",
		"simulated-customer-go",
		"created",
		true,
	)

	want := "postflight_canary_loop_heartbeat{owner=\"digital-guardian-software\",repository=\"simulated-customer-go\",outcome=\"created\"} 1\n" +
		"postflight_canary_loop_last_run_success{owner=\"digital-guardian-software\",repository=\"simulated-customer-go\"} 1\n"
	if body != want {
		t.Fatalf("metrics body = %q, want %q", body, want)
	}
}

func newGitHubServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("X-GitHub-Api-Version"); got != githubAPIVersion {
			t.Fatalf("X-GitHub-Api-Version = %q", got)
		}
		handler(w, req)
	}))
}

func testRunner(apiURL string) *runner {
	return &runner{
		cfg: config{
			apiBaseURL:    apiURL,
			owner:         "test",
			repository:    "canary",
			baseBranch:    "main",
			targetCommit:  "target",
			targetPath:    "target/file.go",
			upstreamPR:    "vercel/turborepo#13426",
			expectedCheck: "targeted-rust-regression",
		},
		gh: &githubClient{
			baseURL:    apiURL,
			token:      "test-token",
			httpClient: http.DefaultClient,
		},
		now: time.Now,
	}
}

func testPullRequest(number int, headSHA string, createdAt time.Time) map[string]any {
	return map[string]any{
		"number":     number,
		"title":      "test(postflight): revert vercel/turborepo#13426",
		"created_at": createdAt.Format(time.RFC3339),
		"head": map[string]any{
			"ref": "postflight-canary/revert-base",
			"sha": headSHA,
			"repo": map[string]string{
				"full_name": "test/canary",
			},
		},
	}
}

func serveValidPullValidation(t *testing.T, w http.ResponseWriter, req *http.Request) bool {
	t.Helper()
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/pulls/23/files":
		writeJSON(t, w, http.StatusOK, []any{map[string]string{"filename": "target/file.go"}})
	case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/git/commits/target":
		writeJSON(t, w, http.StatusOK, testCommit("target", "target-tree", []string{"parent"}))
	case req.Method == http.MethodGet && req.URL.Path == "/repos/test/canary/contents/target/file.go":
		sha := map[string]string{
			"target":   "applied-blob",
			"parent":   "reverted-blob",
			"main":     "applied-blob",
			"head-sha": "reverted-blob",
		}[req.URL.Query().Get("ref")]
		if sha == "" {
			return false
		}
		writeJSON(t, w, http.StatusOK, map[string]string{"type": "file", "sha": sha})
	default:
		return false
	}
	return true
}

func testCommit(sha, tree string, parents []string) map[string]any {
	out := map[string]any{
		"sha":  sha,
		"tree": map[string]string{"sha": tree},
	}
	parentObjects := make([]map[string]string, 0, len(parents))
	for _, parent := range parents {
		parentObjects = append(parentObjects, map[string]string{"sha": parent})
	}
	out["parents"] = parentObjects
	return out
}

func decodeJSON(t *testing.T, req *http.Request, out any) {
	t.Helper()
	defer req.Body.Close()
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(out); err != nil {
		t.Fatalf("decode %s %s: %v", req.Method, req.URL.Path, err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
