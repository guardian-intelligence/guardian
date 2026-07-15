package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is a stateful in-process Actions API: dispatch creates a run,
// every run-list read advances each run one lifecycle step
// (queued -> in_progress -> completed/success), cancel and rerun behave like
// the real endpoints. Deterministic on purpose: a poll loop makes progress
// exactly as fast as it polls.
type fakeGitHub struct {
	t  *testing.T
	mu sync.Mutex

	nextID int64
	runs   map[int64]*fakeRun
	// holdInProgress freezes runs at in_progress so a test's cancel always
	// wins the race against natural completion.
	holdInProgress bool
	// cancelStatus, when non-zero, is answered to every cancel request.
	cancelStatus int

	server *httptest.Server
}

type fakeRun struct {
	id         int64
	workflow   string
	status     string
	conclusion string
	attempt    int64
	createdAt  time.Time
	startedAt  time.Time
	// concluded attempts by number (rerun bumps attempt and re-queues).
	past map[int64]string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, nextID: 1000, runs: map[int64]*fakeRun{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{owner}/{name}/actions/workflows/{workflow}/dispatches", f.dispatch)
	mux.HandleFunc("GET /repos/{owner}/{name}/actions/workflows/{workflow}/runs", f.listRuns)
	mux.HandleFunc("GET /repos/{owner}/{name}/actions/runs/{id}", f.getRun)
	mux.HandleFunc("GET /repos/{owner}/{name}/actions/runs/{id}/attempts/{attempt}", f.getAttempt)
	mux.HandleFunc("GET /repos/{owner}/{name}/actions/runs/{id}/attempts/{attempt}/jobs", f.attemptJobs)
	mux.HandleFunc("GET /repos/{owner}/{name}/actions/runs/{id}/attempts/{attempt}/logs", f.attemptLogs)
	mux.HandleFunc("POST /repos/{owner}/{name}/actions/runs/{id}/cancel", f.cancel)
	mux.HandleFunc("POST /repos/{owner}/{name}/actions/runs/{id}/rerun", f.rerun)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) client(t *testing.T) *ghClient {
	c, err := newGHClient(f.server.URL, "test-token")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

func (f *fakeGitHub) runByID(id int64) *fakeRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[id]
}

func (f *fakeGitHub) pathRun(r *http.Request) *fakeRun {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil
	}
	return f.runs[id]
}

func (f *fakeGitHub) dispatch(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	run := &fakeRun{
		id:        f.nextID,
		workflow:  r.PathValue("workflow"),
		status:    "queued",
		attempt:   1,
		createdAt: time.Now().UTC(),
		past:      map[int64]string{},
	}
	f.runs[run.id] = run
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeGitHub) advanceLocked(run *fakeRun) {
	switch run.status {
	case "queued":
		run.status = "in_progress"
		run.startedAt = time.Now().UTC()
	case "in_progress":
		if f.holdInProgress {
			return
		}
		run.status = "completed"
		run.conclusion = "success"
	}
}

func (f *fakeGitHub) runJSON(run *fakeRun, attempt int64) map[string]any {
	status, conclusion := run.status, run.conclusion
	if attempt != 0 && attempt != run.attempt {
		status = "completed"
		conclusion = run.past[attempt]
	}
	return map[string]any{
		"id":             run.id,
		"event":          "workflow_dispatch",
		"status":         status,
		"conclusion":     conclusion,
		"run_attempt":    run.attempt,
		"created_at":     run.createdAt,
		"run_started_at": run.startedAt,
		"path":           ".github/workflows/" + run.workflow,
		"name":           run.workflow,
	}
}

func (f *fakeGitHub) listRuns(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	workflow := r.PathValue("workflow")
	var out []map[string]any
	for _, run := range f.runs {
		if run.workflow != workflow {
			continue
		}
		f.advanceLocked(run)
		out = append(out, f.runJSON(run, 0))
	}
	writeJSON(w, map[string]any{"total_count": len(out), "workflow_runs": out})
}

func (f *fakeGitHub) getRun(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.pathRun(r)
	if run == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, f.runJSON(run, 0))
}

func (f *fakeGitHub) getAttempt(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.pathRun(r)
	if run == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	attempt, _ := strconv.ParseInt(r.PathValue("attempt"), 10, 64)
	writeJSON(w, f.runJSON(run, attempt))
}

func (f *fakeGitHub) attemptJobs(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.pathRun(r)
	if run == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	attempt, _ := strconv.ParseInt(r.PathValue("attempt"), 10, 64)
	status, conclusion := run.status, run.conclusion
	if attempt != run.attempt {
		status, conclusion = "completed", run.past[attempt]
	}
	job := map[string]any{
		"id":          run.id*10 + attempt,
		"run_id":      run.id,
		"run_attempt": attempt,
		"name":        "build",
		"status":      status,
		"conclusion":  conclusion,
		"runner_name": "hammer-fake-runner",
		"created_at":  run.createdAt,
	}
	if status != "queued" {
		job["started_at"] = run.startedAt
	}
	if status == "completed" {
		completed := run.startedAt.Add(25 * time.Second)
		job["completed_at"] = completed
		job["steps"] = []map[string]any{
			{"name": "Set up job", "number": 1, "status": "completed", "conclusion": conclusion,
				"started_at": run.startedAt, "completed_at": run.startedAt.Add(time.Second)},
			{"name": "Postflight checkout", "number": 2, "status": "completed", "conclusion": conclusion,
				"started_at": run.startedAt.Add(time.Second), "completed_at": run.startedAt.Add(3 * time.Second)},
			{"name": "Build and test", "number": 3, "status": "completed", "conclusion": conclusion,
				"started_at": run.startedAt.Add(3 * time.Second), "completed_at": completed},
		}
	}
	writeJSON(w, map[string]any{"total_count": 1, "jobs": []any{job}})
}

func (f *fakeGitHub) attemptLogs(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	run := f.pathRun(r)
	f.mu.Unlock()
	if run == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, name := range []string{"1_Set up job.txt", "2_Postflight checkout.txt", "3_Build and test.txt"} {
		fw, err := zw.Create("build/" + name)
		if err != nil {
			f.t.Fatalf("zip: %v", err)
		}
		fmt.Fprintf(fw, "log line for step %d\n", i+1)
	}
	if err := zw.Close(); err != nil {
		f.t.Fatalf("zip close: %v", err)
	}
	w.Header().Set("Content-Type", "application/zip")
	_, _ = w.Write(buf.Bytes())
}

func (f *fakeGitHub) cancel(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelStatus != 0 {
		w.WriteHeader(f.cancelStatus)
		return
	}
	run := f.pathRun(r)
	if run == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if run.status == "completed" {
		w.WriteHeader(http.StatusConflict)
		return
	}
	run.status = "completed"
	run.conclusion = "cancelled"
	w.WriteHeader(http.StatusAccepted)
}

func (f *fakeGitHub) rerun(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.pathRun(r)
	if run == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if run.status != "completed" {
		w.WriteHeader(http.StatusConflict)
		return
	}
	run.past[run.attempt] = run.conclusion
	run.attempt++
	run.status = "queued"
	run.conclusion = ""
	w.WriteHeader(http.StatusCreated)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
