package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sync"
	"time"
)

type dispatchConfig struct {
	repo         string
	workflow     string
	ref          string
	pattern      string
	n            int
	ratePerMin   float64
	twinWorkflow string
	twinN        int
	churnMaxWait time.Duration
	restartCmd   string
	dbDSN        string
	statePath    string
}

type dispatcher struct {
	gh           *ghClient
	cfg          dispatchConfig
	st           *stateFile
	pollInterval time.Duration
	rnd          *rand.Rand
}

func runDispatch(ctx context.Context, gh *ghClient, cfg dispatchConfig) error {
	st, err := loadOrInitState(cfg.statePath, cfg.repo, cfg.workflow, time.Now().UTC())
	if err != nil {
		return err
	}
	st.Ref = cfg.ref
	if cfg.twinWorkflow != "" {
		st.TwinWorkflow = cfg.twinWorkflow
	}
	d := &dispatcher{
		gh:           gh,
		cfg:          cfg,
		st:           st,
		pollInterval: 2 * time.Second,
		rnd:          rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	if err := d.captureBaseline(ctx); err != nil {
		return err
	}
	if err := d.run(ctx); err != nil {
		// Partial batteries are still worth reporting on; persist what fired.
		_ = saveState(cfg.statePath, st)
		return err
	}
	return saveState(cfg.statePath, st)
}

// captureBaseline snapshots slot and generation accounting before the first
// dispatch; the report's balance assertions compare the post-battery state
// against exactly this.
func (d *dispatcher) captureBaseline(ctx context.Context) error {
	if d.cfg.dbDSN == "" || d.st.Baseline != nil {
		return nil
	}
	db, err := openDB(ctx, d.cfg.dbDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	snap, err := db.Snapshot(ctx)
	if err != nil {
		return err
	}
	d.st.Baseline = snap
	return nil
}

func (d *dispatcher) run(ctx context.Context) error {
	var err error
	switch d.cfg.pattern {
	case "burst":
		err = d.burst(ctx, d.cfg.n)
	case "sustained":
		err = d.sustained(ctx)
	case "churn":
		err = d.churn(ctx)
	case "restart":
		err = d.restart(ctx)
	default:
		return fmt.Errorf("unknown pattern %q (want burst, sustained, churn, or restart)", d.cfg.pattern)
	}
	if err != nil {
		return err
	}
	return d.dispatchTwins(ctx)
}

func (d *dispatcher) dispatchOne(ctx context.Context, workflow string, twin bool) error {
	if err := d.gh.dispatchWorkflow(ctx, d.cfg.repo, workflow, d.cfg.ref); err != nil {
		return err
	}
	d.st.Dispatches = append(d.st.Dispatches, dispatchRecord{
		Pattern:      d.cfg.pattern,
		Workflow:     workflow,
		Twin:         twin,
		DispatchedAt: time.Now().UTC(),
	})
	return nil
}

func (d *dispatcher) burst(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		if err := d.dispatchOne(ctx, d.cfg.workflow, false); err != nil {
			return err
		}
	}
	return nil
}

func (d *dispatcher) sustained(ctx context.Context) error {
	if d.cfg.ratePerMin <= 0 {
		return fmt.Errorf("sustained pattern needs -rate > 0")
	}
	interval := time.Duration(float64(time.Minute) / d.cfg.ratePerMin)
	for i := 0; i < d.cfg.n; i++ {
		if i > 0 {
			if err := sleepCtx(ctx, interval); err != nil {
				return err
			}
		}
		if err := d.dispatchOne(ctx, d.cfg.workflow, false); err != nil {
			return err
		}
	}
	return nil
}

// churn dispatches serially so each new run is unambiguously the one just
// fired, then cancels it at a random point in flight and re-runs it. The
// cancel/rerun tail runs concurrently per run; the next dispatch only waits
// for run correlation.
func (d *dispatcher) churn(ctx context.Context) error {
	known := map[int64]bool{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for i := 0; i < d.cfg.n; i++ {
		if err := d.dispatchOne(ctx, d.cfg.workflow, false); err != nil {
			wg.Wait()
			return err
		}
		run, err := d.awaitNewRun(ctx, known)
		if err != nil {
			wg.Wait()
			return err
		}
		known[run.ID] = true
		wait := time.Duration(d.rnd.Int63n(int64(d.cfg.churnMaxWait) + 1))
		wg.Add(1)
		go func(runID int64, wait time.Duration) {
			defer wg.Done()
			rec, err := d.churnOne(ctx, runID, wait)
			mu.Lock()
			defer mu.Unlock()
			if rec != nil {
				d.st.Churn = append(d.st.Churn, *rec)
			}
			if err != nil {
				errs = append(errs, err)
			}
		}(run.ID, wait)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("churn: %d of %d cancel/rerun cycles failed: %v", len(errs), d.cfg.n, errs[0])
	}
	return nil
}

func (d *dispatcher) churnOne(ctx context.Context, runID int64, wait time.Duration) (*churnRecord, error) {
	if err := sleepCtx(ctx, wait); err != nil {
		return nil, err
	}
	rec := &churnRecord{RunID: runID, CancelledAt: time.Now().UTC(), CancelAttempt: 1}
	if run, err := d.gh.getRun(ctx, d.cfg.repo, runID); err == nil && run.RunAttempt > 0 {
		rec.CancelAttempt = run.RunAttempt
	}
	if err := d.gh.cancelRun(ctx, d.cfg.repo, runID); err != nil {
		// A cancel that loses the race to natural completion is a valid churn
		// outcome: nothing to rerun, nothing to assert.
		return rec, nil
	}
	rec.CancelConfirmed = true
	if err := d.awaitRunTerminal(ctx, runID); err != nil {
		return rec, err
	}
	if err := d.gh.rerunRun(ctx, d.cfg.repo, runID); err != nil {
		return rec, fmt.Errorf("rerun run %d: %w", runID, err)
	}
	now := time.Now().UTC()
	rec.RerunAt = &now
	return rec, nil
}

// awaitNewRun polls the workflow's run list until a run not yet claimed by a
// previous dispatch appears. workflow_dispatch returns no run id, so
// appearance order is the only correlation there is — which is why churn
// dispatches serially.
func (d *dispatcher) awaitNewRun(ctx context.Context, known map[int64]bool) (ghRun, error) {
	since := d.st.StartedAt.Add(-2 * time.Minute)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		runs, err := d.gh.listWorkflowRuns(ctx, d.cfg.repo, d.cfg.workflow, since)
		if err != nil {
			return ghRun{}, err
		}
		var newest *ghRun
		for i := range runs {
			r := runs[i]
			if known[r.ID] {
				continue
			}
			if newest == nil || r.CreatedAt.After(newest.CreatedAt) {
				newest = &r
			}
		}
		if newest != nil {
			return *newest, nil
		}
		if time.Now().After(deadline) {
			return ghRun{}, fmt.Errorf("dispatched run never appeared in %s's run list", d.cfg.workflow)
		}
		if err := sleepCtx(ctx, d.pollInterval); err != nil {
			return ghRun{}, err
		}
	}
}

func (d *dispatcher) awaitRunTerminal(ctx context.Context, runID int64) error {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		run, err := d.gh.getRun(ctx, d.cfg.repo, runID)
		if err == nil && run.Status == "completed" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("run %d never reached a terminal status after cancel", runID)
		}
		if err := sleepCtx(ctx, d.pollInterval); err != nil {
			return err
		}
	}
}

// restart fires a burst, waits for roughly half of it to be visibly in
// flight, then runs the operator-supplied restart command exactly once.
func (d *dispatcher) restart(ctx context.Context) error {
	if err := d.burst(ctx, d.cfg.n); err != nil {
		return err
	}
	if d.cfg.restartCmd == "" {
		fmt.Fprintln(os.Stderr, "hammer: HAMMER_RESTART_CMD is unset; restart pattern dispatched load but skipped the restart")
		return nil
	}
	if err := d.awaitInFlight(ctx, (d.cfg.n+1)/2); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", d.cfg.restartCmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("HAMMER_RESTART_CMD: %w", err)
	}
	now := time.Now().UTC()
	d.st.RestartAt = &now
	return nil
}

func (d *dispatcher) awaitInFlight(ctx context.Context, want int) error {
	since := d.st.StartedAt.Add(-2 * time.Minute)
	deadline := time.Now().Add(5 * time.Minute)
	for {
		runs, err := d.gh.listWorkflowRuns(ctx, d.cfg.repo, d.cfg.workflow, since)
		if err != nil {
			return err
		}
		started := 0
		for _, r := range runs {
			if r.Status == "in_progress" || r.Status == "completed" {
				started++
			}
		}
		if started >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("only %d of %d runs in flight before the restart window closed", started, want)
		}
		if err := sleepCtx(ctx, d.pollInterval); err != nil {
			return err
		}
	}
}

func (d *dispatcher) dispatchTwins(ctx context.Context) error {
	if d.cfg.twinWorkflow == "" || d.cfg.twinN <= 0 {
		return nil
	}
	for i := 0; i < d.cfg.twinN; i++ {
		if err := d.dispatchOne(ctx, d.cfg.twinWorkflow, true); err != nil {
			return err
		}
	}
	return nil
}
