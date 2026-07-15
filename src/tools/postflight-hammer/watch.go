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
	st, err := loadState(cfg.statePath)
	if err != nil {
		return err
	}
	if len(st.Dispatches) == 0 {
		return fmt.Errorf("state file %s records no dispatches; run `postflight-hammer dispatch` first", cfg.statePath)
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
// every dispatched run is terminal (logs captured) and no demand or lease is
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

func (w *watcher) observeGitHub(ctx context.Context) (bool, error) {
	since := w.st.StartedAt.Add(-2 * time.Minute)
	expected := map[string]int{}
	for _, d := range w.st.Dispatches {
		expected[d.Workflow]++
	}
	seen := map[string]int{}
	allTerminal := true
	for workflow := range expected {
		runs, err := w.gh.listWorkflowRuns(ctx, w.cfg.repo, workflow, since)
		if err != nil {
			return false, err
		}
		for _, r := range runs {
			seen[workflow]++
			rec := w.recordRun(r, workflow)
			if err := w.observeAttempts(ctx, rec); err != nil {
				return false, err
			}
			if rec.Status != "completed" {
				allTerminal = false
			}
		}
	}
	for workflow, want := range expected {
		if seen[workflow] < want {
			allTerminal = false
		}
	}
	return allTerminal, nil
}

func (w *watcher) recordRun(r ghRun, workflow string) *runRecord {
	key := fmt.Sprintf("%d", r.ID)
	rec := w.st.Runs[key]
	if rec == nil {
		rec = &runRecord{RunID: r.ID, Attempts: map[string]*attemptRecord{}}
		w.st.Runs[key] = rec
	}
	rec.Workflow = workflow
	rec.Twin = workflow == w.st.TwinWorkflow && workflow != w.st.Workflow
	rec.CreatedAt = r.CreatedAt
	rec.LatestAttempt = r.RunAttempt
	rec.Status = r.Status
	rec.Conclusion = r.Conclusion
	return rec
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
			Demands:    map[string]demandRow{},
			Leases:     map[string]leaseRow{},
			Deliveries: map[string]time.Time{},
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

	leases, err := w.db.LeasesSince(ctx, since)
	if err != nil {
		return false, err
	}
	for _, l := range leases {
		prev, seen := o.Leases[l.LeaseID]
		if !seen || prev.State != l.State {
			o.Transitions = append(o.Transitions, transition{
				Kind: "lease", ID: l.LeaseID, Field: "state", Value: l.State, ObservedAt: now,
			})
		}
		if l.ReportedState != "" && (!seen || prev.ReportedState != l.ReportedState) {
			o.Transitions = append(o.Transitions, transition{
				Kind: "lease", ID: l.LeaseID, Field: "reported_state", Value: l.ReportedState, ObservedAt: now,
			})
		}
		o.Leases[l.LeaseID] = l
		if !terminalLeaseStates[l.State] {
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
