package main

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDispatcher(t *testing.T, f *fakeGitHub, cfg dispatchConfig) *dispatcher {
	st, err := loadOrInitState(cfg.statePath, cfg.repo, cfg.workflow, time.Now().UTC())
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	st.Ref = cfg.ref
	if cfg.twinWorkflow != "" {
		st.TwinWorkflow = cfg.twinWorkflow
	}
	return &dispatcher{
		gh:           f.client(t),
		cfg:          cfg,
		st:           st,
		pollInterval: 10 * time.Millisecond,
		awaitTimeout: 5 * time.Second,
		rnd:          rand.New(rand.NewSource(1)),
	}
}

// completeRun dispatches one run and drives it to completed/success (each
// run-list read advances the fake one lifecycle step).
func completeRun(t *testing.T, f *fakeGitHub, workflow string) int64 {
	t.Helper()
	c := f.client(t)
	ctx := context.Background()
	if err := c.dispatchWorkflow(ctx, "acme/demo", workflow, "main", nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var id int64
	for i := 0; i < 2; i++ {
		runs, err := c.listWorkflowRuns(ctx, "acme/demo", workflow, time.Now().Add(-time.Hour))
		if err != nil || len(runs) == 0 {
			t.Fatalf("list: %v (%d runs)", err, len(runs))
		}
		for _, r := range runs {
			if r.ID > id {
				id = r.ID
			}
		}
	}
	run := f.runByID(id)
	if run == nil || run.status != "completed" {
		t.Fatalf("run %d not completed: %+v", id, run)
	}
	return id
}

func TestBurstPattern(t *testing.T) {
	f := newFakeGitHub(t)
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "burst", n: 3,
		statePath: filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("burst: %v", err)
	}
	if len(d.st.Dispatches) != 3 {
		t.Fatalf("recorded %d dispatches, want 3", len(d.st.Dispatches))
	}
	if len(d.st.Runs) != 3 {
		t.Fatalf("claimed %d runs, want 3", len(d.st.Runs))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) != 3 {
		t.Fatalf("fake got %d runs, want 3", len(f.runs))
	}
}

func TestSustainedPattern(t *testing.T) {
	f := newFakeGitHub(t)
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "sustained", n: 3,
		ratePerMin: 60000, // ~1ms interval
		statePath:  filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("sustained: %v", err)
	}
	if len(d.st.Dispatches) != 3 {
		t.Fatalf("recorded %d dispatches, want 3", len(d.st.Dispatches))
	}
}

func TestSerialPatternWaitsForEachRun(t *testing.T) {
	f := newFakeGitHub(t)
	f.advanceOnGet = true
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "serial", n: 3,
		inputs:    workflowInputs{"lane": "postflight", "cache_epoch": "cohort-1"},
		statePath: filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("serial: %v", err)
	}
	if f.overlapped {
		t.Fatal("serial pattern dispatched a successor before its predecessor completed")
	}
	if len(d.st.Dispatches) != 3 || len(d.st.Runs) != 3 {
		t.Fatalf("serial recorded %d dispatches and %d runs", len(d.st.Dispatches), len(d.st.Runs))
	}
	for _, rec := range d.st.Dispatches {
		if rec.Inputs["lane"] != "postflight" || rec.Inputs["cache_epoch"] != "cohort-1" {
			t.Fatalf("dispatch inputs not retained: %+v", rec.Inputs)
		}
	}
	for _, run := range d.st.Runs {
		if run.Inputs["lane"] != "postflight" || run.Inputs["cache_epoch"] != "cohort-1" {
			t.Fatalf("run inputs not retained: %+v", run.Inputs)
		}
	}
}

func TestChurnCancelsAndReruns(t *testing.T) {
	f := newFakeGitHub(t)
	f.holdInProgress = true
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "churn", n: 2,
		churnMaxWait: time.Millisecond,
		statePath:    filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("churn: %v", err)
	}
	if len(d.st.Churn) != 2 {
		t.Fatalf("recorded %d churn cycles, want 2", len(d.st.Churn))
	}
	for _, c := range d.st.Churn {
		if !c.CancelConfirmed {
			t.Fatalf("churn run %d: cancel not confirmed", c.RunID)
		}
		if c.RerunAt == nil {
			t.Fatalf("churn run %d: never re-run", c.RunID)
		}
		run := f.runByID(c.RunID)
		if run == nil || run.attempt != 2 {
			t.Fatalf("churn run %d: fake attempt = %+v, want 2", c.RunID, run)
		}
		if run.past[1] != "cancelled" {
			t.Fatalf("churn run %d: attempt 1 concluded %q, want cancelled", c.RunID, run.past[1])
		}
	}
}

// A battery accumulates patterns into one state file, so churn correlation
// must never adopt a run an earlier pattern left behind.
func TestChurnIgnoresPreexistingRuns(t *testing.T) {
	f := newFakeGitHub(t)
	oldID := completeRun(t, f, "ci.yml")
	f.holdInProgress = true

	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "churn", n: 1,
		churnMaxWait: time.Millisecond,
		statePath:    filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("churn: %v", err)
	}
	if len(d.st.Churn) != 1 {
		t.Fatalf("churn records = %+v", d.st.Churn)
	}
	if d.st.Churn[0].RunID == oldID {
		t.Fatal("churn adopted a pre-existing run")
	}
	if !d.st.Churn[0].CancelConfirmed || d.st.Churn[0].RerunAt == nil {
		t.Fatalf("churn cycle incomplete: %+v", d.st.Churn[0])
	}
	if old := f.runByID(oldID); old.attempt != 1 || old.conclusion != "success" {
		t.Fatalf("pre-existing run was touched: %+v", old)
	}
}

// The churn pattern must fail loudly when cancels are denied (bad credential,
// missing actions:write) instead of degrading into a plain burst.
func TestChurnFailsOnCancelDenied(t *testing.T) {
	f := newFakeGitHub(t)
	f.holdInProgress = true
	f.cancelStatus = 403
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "churn", n: 1,
		churnMaxWait: time.Millisecond,
		statePath:    filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	err := d.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cancel run") {
		t.Fatalf("denied cancel did not fail the pattern: %v", err)
	}
}

func TestRestartPatternRunsCommandOnce(t *testing.T) {
	f := newFakeGitHub(t)
	f.holdInProgress = true
	marker := filepath.Join(t.TempDir(), "restarted")
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "restart", n: 2,
		restartCmd: "echo restarted > " + marker,
		statePath:  filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if d.st.RestartAt == nil {
		t.Fatal("restart time not recorded")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("restart command never ran: %v", err)
	}
}

// The restart must land while the load is still running; a battery whose
// runs all completed first has not exercised the scenario and must fail.
func TestRestartFailsWhenLoadDrainsFirst(t *testing.T) {
	f := newFakeGitHub(t)
	marker := filepath.Join(t.TempDir(), "restarted")
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "restart", n: 2,
		restartCmd: "echo restarted > " + marker,
		statePath:  filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	err := d.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "completed before the restart") {
		t.Fatalf("drained load did not fail the pattern: %v", err)
	}
	if d.st.RestartAt != nil {
		t.Fatal("restart fired against an idle host")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("restart command ran against an idle host")
	}
}

func TestRestartPatternSkippedWithoutCommand(t *testing.T) {
	f := newFakeGitHub(t)
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "restart", n: 1,
		statePath: filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if d.st.RestartAt != nil {
		t.Fatal("restart should be skipped when HAMMER_RESTART_CMD is unset")
	}
	if len(d.st.Dispatches) != 1 {
		t.Fatalf("load should still fire; got %d dispatches", len(d.st.Dispatches))
	}
}

func TestTwinDispatches(t *testing.T) {
	f := newFakeGitHub(t)
	cfg := dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "burst", n: 1,
		twinWorkflow: "twin.yml", twinN: 2,
		inputs:     workflowInputs{"lane": "postflight"},
		twinInputs: workflowInputs{"lane": "github"},
		statePath:  filepath.Join(t.TempDir(), "state.json"),
	}
	d := testDispatcher(t, f, cfg)
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	twins := 0
	for _, rec := range d.st.Dispatches {
		if rec.Twin {
			twins++
			if rec.Workflow != "twin.yml" {
				t.Fatalf("twin dispatch on %s", rec.Workflow)
			}
			if rec.Inputs["lane"] != "github" {
				t.Fatalf("twin inputs = %+v", rec.Inputs)
			}
		} else if rec.Inputs["lane"] != "postflight" {
			t.Fatalf("primary inputs = %+v", rec.Inputs)
		}
	}
	if twins != 2 {
		t.Fatalf("got %d twin dispatches, want 2", twins)
	}
}

func TestStateLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	unlock, err := lockState(path)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, err := lockState(path); err == nil {
		t.Fatal("second lock on a held state file should refuse")
	}
	unlock()
	unlock2, err := lockState(path)
	if err != nil {
		t.Fatalf("relock after release: %v", err)
	}
	unlock2()
}

func TestStateFileRefusesForeignBattery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := &stateFile{Repo: "acme/demo", Workflow: "ci.yml", StartedAt: time.Now(), Runs: map[string]*runRecord{}}
	if err := saveState(path, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := loadOrInitState(path, "acme/demo", "other.yml", time.Now()); err == nil {
		t.Fatal("mismatched workflow should be refused")
	}
	if _, err := loadOrInitState(path, "acme/demo", "ci.yml", time.Now()); err != nil {
		t.Fatalf("matching battery should load: %v", err)
	}
}
