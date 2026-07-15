package main

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
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
		rnd:          rand.New(rand.NewSource(1)),
	}
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

func TestRestartPatternRunsCommandOnce(t *testing.T) {
	f := newFakeGitHub(t)
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
		statePath: filepath.Join(t.TempDir(), "state.json"),
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
		}
	}
	if twins != 2 {
		t.Fatalf("got %d twin dispatches, want 2", twins)
	}
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
