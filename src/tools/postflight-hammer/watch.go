package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

type watchConfig struct {
	repo      string
	statePath string
	dbDSN     string
	poll      time.Duration
	timeout   time.Duration
	settle    time.Duration
}

type watcher struct {
	gh  *ghClient
	db  *dbClient
	cfg watchConfig
	st  *stateFile
	now func() time.Time
}

func runWatch(ctx context.Context, gh *ghClient, cfg watchConfig) error {
	unlock, err := lockState(cfg.statePath)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := loadState(cfg.statePath)
	if err != nil {
		return err
	}
	if len(st.Dispatches) == 0 {
		return fmt.Errorf("state file %s records no dispatches; run `postflight-hammer dispatch` first", cfg.statePath)
	}
	if len(st.Runs) == 0 {
		return fmt.Errorf("state file %s records %d dispatches but no correlated runs; the dispatch failed before correlation", cfg.statePath, len(st.Dispatches))
	}
	if len(st.Runs) != len(st.Dispatches) {
		fmt.Fprintf(os.Stderr, "hammer: %d dispatches but %d correlated runs; the report will fail the battery\n", len(st.Dispatches), len(st.Runs))
	}
	w := &watcher{gh: gh, cfg: cfg, st: st, now: time.Now}
	if cfg.dbDSN != "" {
		if w.db, err = openDB(ctx, cfg.dbDSN); err != nil {
			return err
		}
		defer w.db.Close()
	}
	deadline := w.now().Add(cfg.timeout)
	for {
		done, err := w.tick(ctx)
		if err != nil {
			_ = saveState(cfg.statePath, st)
			return err
		}
		if err := saveState(cfg.statePath, st); err != nil {
			return err
		}
		if done {
			break
		}
		if w.now().After(deadline) {
			return fmt.Errorf("watch timed out after %s with runs or database rows still non-terminal", cfg.timeout)
		}
		if err := sleepCtx(ctx, cfg.poll); err != nil {
			return err
		}
	}
	// Seals, slot releases, and pool refills land after the last run turns
	// terminal; the settle window lets the books close before the final
	// snapshot the report asserts against.
	if err := sleepCtx(ctx, cfg.settle); err != nil {
		return err
	}
	if _, err := w.tick(ctx); err != nil {
		return err
	}
	if err := w.finalize(ctx); err != nil {
		return err
	}
	return saveState(cfg.statePath, st)
}

// tick performs one observation pass over GitHub and the database; done means
// every dispatched run is terminal (logs captured) and no demand or assignment is
// still in flight.
func (w *watcher) tick(ctx context.Context) (bool, error) {
	runsDone, err := w.observeGitHub(ctx)
	if err != nil {
		return false, err
	}
	dbDone := true
	if w.db != nil {
		if dbDone, err = w.observeDB(ctx); err != nil {
			return false, err
		}
	}
	return runsDone && dbDone, nil
}

// observeGitHub refreshes exactly the runs dispatch claimed: a run someone
// else fired into the same window is never adopted into the battery, and a
// claimed run stuck non-terminal keeps the watch open.
func (w *watcher) observeGitHub(ctx context.Context) (bool, error) {
	since := w.st.StartedAt.Add(-2 * time.Minute)
	workflows := map[string]bool{}
	for _, rec := range w.st.Runs {
		workflows[rec.Workflow] = true
	}
	for workflow := range workflows {
		runs, err := w.gh.listWorkflowRuns(ctx, w.cfg.repo, workflow, since)
		if err != nil {
			return false, err
		}
		for _, r := range runs {
			rec := w.st.Runs[fmt.Sprintf("%d", r.ID)]
			if rec == nil {
				continue
			}
			rec.CreatedAt = r.CreatedAt
			rec.LatestAttempt = r.RunAttempt
			rec.Status = r.Status
			rec.Conclusion = r.Conclusion
		}
	}
	allTerminal := true
	for _, rec := range w.st.Runs {
		if err := w.observeAttempts(ctx, rec); err != nil {
			return false, err
		}
		if rec.Status != "completed" {
			allTerminal = false
		}
	}
	return allTerminal, nil
}

// observeAttempts refreshes every attempt of the run that is not yet fully
// recorded, and fetches the log archive exactly once per terminal attempt.
func (w *watcher) observeAttempts(ctx context.Context, rec *runRecord) error {
	for n := int64(1); n <= rec.LatestAttempt; n++ {
		a := rec.attempt(n)
		if a.terminal() && a.LogsFetched {
			continue
		}
		if !a.terminal() {
			run, err := w.gh.runAttempt(ctx, w.cfg.repo, rec.RunID, n)
			if err != nil {
				return err
			}
			a.Status = run.Status
			a.Conclusion = run.Conclusion
			a.StartedAt = run.RunStartedAt
			jobs, err := w.gh.attemptJobs(ctx, w.cfg.repo, rec.RunID, n)
			if err != nil {
				return err
			}
			a.Jobs = a.Jobs[:0]
			for _, j := range jobs {
				jr := jobRecord{
					JobID:       j.ID,
					Name:        j.Name,
					Status:      j.Status,
					Conclusion:  j.Conclusion,
					RunnerName:  j.RunnerName,
					CreatedAt:   j.CreatedAt,
					StartedAt:   j.StartedAt,
					CompletedAt: j.CompletedAt,
				}
				for _, s := range j.Steps {
					jr.Steps = append(jr.Steps, stepRecord{
						Number:      s.Number,
						Name:        s.Name,
						Status:      s.Status,
						Conclusion:  s.Conclusion,
						StartedAt:   s.StartedAt,
						CompletedAt: s.CompletedAt,
					})
				}
				a.Jobs = append(a.Jobs, jr)
			}
		}
		if a.terminal() && !a.LogsFetched {
			sizes, err := w.gh.attemptLogs(ctx, w.cfg.repo, rec.RunID, n)
			if err != nil {
				// Cancelled attempts can have no archive at all; that is not
				// an observation failure, and the report treats missing logs
				// on a required attempt as the assertion failure it is.
				fmt.Fprintf(os.Stderr, "hammer: logs for run %d attempt %d: %v\n", rec.RunID, n, err)
				sizes = map[string]int64{}
			}
			a.StepLogBytes = sizes
			a.LogsFetched = true
		}
	}
	return nil
}

func (w *watcher) observeDB(ctx context.Context) (bool, error) {
	if w.st.DB == nil {
		w.st.DB = &dbObservations{
			Demands:     map[string]demandRow{},
			Assignments: map[string]assignmentRow{},
			Deliveries:  map[string]time.Time{},
		}
	}
	o := w.st.DB
	now := w.now().UTC()
	since := w.st.StartedAt.Add(-time.Minute)

	demands, err := w.db.DemandsSince(ctx, since)
	if err != nil {
		return false, err
	}
	quiescent := true
	for _, d := range demands {
		key := fmt.Sprintf("%d", d.ProviderJobID)
		prev, seen := o.Demands[key]
		if !seen || prev.State != d.State {
			o.Transitions = append(o.Transitions, transition{
				Kind: "demand", ID: key, Field: "state", Value: d.State, ObservedAt: now,
			})
		}
		o.Demands[key] = d
		if !terminalDemandStates[d.State] {
			quiescent = false
		}
	}

	assignments, err := w.db.AssignmentsSince(ctx, since)
	if err != nil {
		return false, err
	}
	for _, assignment := range assignments {
		prev, seen := o.Assignments[assignment.AssignmentID]
		if !seen || prev.State != assignment.State {
			o.Transitions = append(o.Transitions, transition{
				Kind: "assignment", ID: assignment.AssignmentID, Field: "state", Value: assignment.State, ObservedAt: now,
			})
		}
		if assignment.RestoreOutcome != "" && (!seen || prev.RestoreOutcome != assignment.RestoreOutcome) {
			o.Transitions = append(o.Transitions, transition{
				Kind: "assignment", ID: assignment.AssignmentID, Field: "restore_outcome", Value: assignment.RestoreOutcome, ObservedAt: now,
			})
		}
		o.Assignments[assignment.AssignmentID] = assignment
		if !terminalAssignmentStates[assignment.State] {
			quiescent = false
		}
	}

	deliveries, err := w.db.DeliveriesSince(ctx, since)
	if err != nil {
		return false, err
	}
	for job, at := range deliveries {
		if _, seen := o.Deliveries[job]; !seen {
			o.Deliveries[job] = at
		}
	}

	generations, err := w.db.Generations(ctx)
	if err != nil {
		return false, err
	}
	for _, g := range generations {
		if _, seen := o.observedAt("generation", g.Generation, "state", g.State); !seen {
			o.Transitions = append(o.Transitions, transition{
				Kind: "generation", ID: g.Generation, Field: "state", Value: g.State, ObservedAt: now,
			})
		}
	}
	return quiescent, nil
}

func (w *watcher) finalize(ctx context.Context) error {
	now := w.now().UTC()
	w.st.WatchDoneAt = &now
	if w.db == nil {
		return nil
	}
	if w.st.DB == nil {
		w.st.DB = &dbObservations{}
	}
	snap, err := w.db.Snapshot(ctx)
	if err != nil {
		return err
	}
	w.st.DB.Final = snap
	return nil
}
