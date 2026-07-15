package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchRunsToTerminalAndFetchesLogs(t *testing.T) {
	f := newFakeGitHub(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	d := testDispatcher(t, f, dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "burst", n: 2,
		twinWorkflow: "twin.yml", twinN: 1,
		statePath: statePath,
	})
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := saveState(statePath, d.st); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := watchConfig{
		repo:      "acme/demo",
		statePath: statePath,
		poll:      10 * time.Millisecond,
		timeout:   30 * time.Second,
		settle:    10 * time.Millisecond,
	}
	if err := runWatch(context.Background(), f.client(t), cfg); err != nil {
		t.Fatalf("watch: %v", err)
	}

	st, err := loadState(statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if st.WatchDoneAt == nil {
		t.Fatal("watch did not record completion")
	}
	if len(st.Runs) != 3 {
		t.Fatalf("observed %d runs, want 3", len(st.Runs))
	}
	twins := 0
	for _, run := range st.Runs {
		if run.Twin {
			twins++
		}
		if run.Status != "completed" || run.Conclusion != "success" {
			t.Fatalf("run %d: %s/%s", run.RunID, run.Status, run.Conclusion)
		}
		for _, a := range run.Attempts {
			if !a.LogsFetched || len(a.StepLogBytes) != 3 {
				t.Fatalf("run %d attempt %d: logs=%v %v", run.RunID, a.Attempt, a.LogsFetched, a.StepLogBytes)
			}
			if len(a.Jobs) != 1 || len(a.Jobs[0].Steps) != 3 {
				t.Fatalf("run %d attempt %d: jobs %+v", run.RunID, a.Attempt, a.Jobs)
			}
		}
	}
	if twins != 1 {
		t.Fatalf("observed %d twin runs, want 1", twins)
	}
}

// A run someone else fired into the battery's window must never be adopted:
// it can neither satisfy dispatch completeness nor fail the scoreboard.
func TestWatchIgnoresForeignRuns(t *testing.T) {
	f := newFakeGitHub(t)
	foreignID := completeRun(t, f, "ci.yml")

	statePath := filepath.Join(t.TempDir(), "state.json")
	d := testDispatcher(t, f, dispatchConfig{
		repo: "acme/demo", workflow: "ci.yml", ref: "main", pattern: "burst", n: 1,
		statePath: statePath,
	})
	if err := d.run(context.Background()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := saveState(statePath, d.st); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := watchConfig{
		repo:      "acme/demo",
		statePath: statePath,
		poll:      10 * time.Millisecond,
		timeout:   30 * time.Second,
		settle:    10 * time.Millisecond,
	}
	if err := runWatch(context.Background(), f.client(t), cfg); err != nil {
		t.Fatalf("watch: %v", err)
	}
	st, err := loadState(statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(st.Runs) != 1 {
		t.Fatalf("observed %d runs, want 1", len(st.Runs))
	}
	for _, run := range st.Runs {
		if run.RunID == foreignID {
			t.Fatal("watch adopted a foreign run")
		}
	}
}

func TestWatchRequiresDispatches(t *testing.T) {
	f := newFakeGitHub(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	st := &stateFile{Repo: "acme/demo", Workflow: "ci.yml", StartedAt: time.Now().UTC(), Runs: map[string]*runRecord{}}
	if err := saveState(statePath, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg := watchConfig{repo: "acme/demo", statePath: statePath, poll: time.Millisecond, timeout: time.Second, settle: time.Millisecond}
	if err := runWatch(context.Background(), f.client(t), cfg); err == nil {
		t.Fatal("watch of an empty battery should refuse")
	}

	st.Dispatches = []dispatchRecord{{Pattern: "burst", Workflow: "ci.yml", DispatchedAt: time.Now().UTC()}}
	if err := saveState(statePath, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := runWatch(context.Background(), f.client(t), cfg); err == nil {
		t.Fatal("watch of a battery with no correlated runs should refuse")
	}
}
