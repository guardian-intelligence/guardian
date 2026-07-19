package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

type dispatchConfig struct {
	repo           string
	workflow       string
	ref            string
	pattern        string
	n              int
	ratePerMin     float64
	inputs         workflowInputs
	twinWorkflow   string
	twinN          int
	twinInputs     workflowInputs
	awaitPromotion bool
	churnMaxWait   time.Duration
	restartCmd     string
	dbDSN          string
	statePath      string
}

type dispatcher struct {
	gh           *ghClient
	cfg          dispatchConfig
	st           *stateFile
	pollInterval time.Duration
	awaitTimeout time.Duration
	rnd          *rand.Rand
}

func runDispatch(ctx context.Context, gh *ghClient, cfg dispatchConfig) error {
	unlock, err := lockState(cfg.statePath)
	if err != nil {
		return err
	}
	defer unlock()
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
		awaitTimeout: 5 * time.Minute,
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
		_, err = d.fireAndCorrelate(ctx, d.cfg.workflow, false, d.cfg.n, 0)
	case "serial":
		err = d.serial(ctx, d.cfg.workflow, false, d.cfg.n, d.cfg.awaitPromotion)
	case "sustained":
		err = d.sustained(ctx)
	case "churn":
		err = d.churn(ctx)
	case "restart":
		err = d.restart(ctx)
	default:
		return fmt.Errorf("unknown pattern %q (want burst, serial, sustained, churn, or restart)", d.cfg.pattern)
	}
	if err != nil {
		return err
	}
	return d.dispatchTwins(ctx)
}

func (d *dispatcher) dispatchOne(ctx context.Context, workflow string, twin bool) error {
	inputs := d.cfg.inputs
	if twin {
		inputs = d.cfg.twinInputs
	}
	if err := d.gh.dispatchWorkflow(ctx, d.cfg.repo, workflow, d.cfg.ref, inputs); err != nil {
		return err
	}
	d.st.Dispatches = append(d.st.Dispatches, dispatchRecord{
		Pattern:      d.cfg.pattern,
		Workflow:     workflow,
		Inputs:       cloneInputs(inputs),
		Twin:         twin,
		DispatchedAt: time.Now().UTC(),
	})
	return nil
}

// serial waits for each run to complete before dispatching its successor.
// For Postflight cache benchmarks, awaitPromotion additionally proves the
// completed run's sealed generation is current before the next lease begins.
func (d *dispatcher) serial(ctx context.Context, workflow string, twin bool, n int, awaitPromotion bool) error {
	known, err := d.knownRunIDs(ctx, workflow)
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if err := d.dispatchOne(ctx, workflow, twin); err != nil {
			return err
		}
		runs, err := d.awaitNewRuns(ctx, workflow, known, 1)
		if err != nil {
			return err
		}
		run := runs[0]
		d.claimRun(run, workflow, twin)
		run, err = d.awaitRunTerminal(ctx, run.ID)
		if err != nil {
			return err
		}
		if run.Conclusion != "success" {
			return fmt.Errorf("run %d completed with conclusion %q", run.ID, run.Conclusion)
		}
		if awaitPromotion {
			if err := d.awaitRunPromotion(ctx, run.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *dispatcher) sustained(ctx context.Context) error {
	if d.cfg.ratePerMin <= 0 {
		return fmt.Errorf("sustained pattern needs -rate > 0")
	}
	interval := time.Duration(float64(time.Minute) / d.cfg.ratePerMin)
	_, err := d.fireAndCorrelate(ctx, d.cfg.workflow, false, d.cfg.n, interval)
	return err
}

// knownRunIDs is the set a new-run correlation must never adopt: everything
// the workflow's run list shows right now, plus every run this battery has
// already claimed — a run an earlier pattern fired may become visible only
// after this snapshot, and it must still not be adopted.
func (d *dispatcher) knownRunIDs(ctx context.Context, workflow string) (map[int64]bool, error) {
	runs, err := d.gh.listWorkflowRuns(ctx, d.cfg.repo, workflow, d.st.StartedAt.Add(-2*time.Minute))
	if err != nil {
		return nil, err
	}
	known := map[int64]bool{}
	for _, r := range runs {
		known[r.ID] = true
	}
	for _, rec := range d.st.Runs {
		known[rec.RunID] = true
	}
	return known, nil
}

// claimRun records the run as a member of the battery. Watch refreshes only
// claimed runs and report asserts dispatch and claim counts match, so a run
// never claimed here is invisible and a dropped dispatch cannot be masked by
// someone else's run.
func (d *dispatcher) claimRun(r ghRun, workflow string, twin bool) {
	inputs := d.cfg.inputs
	if twin {
		inputs = d.cfg.twinInputs
	}
	d.st.Runs[fmt.Sprintf("%d", r.ID)] = &runRecord{
		RunID:         r.ID,
		Workflow:      workflow,
		Inputs:        cloneInputs(inputs),
		Twin:          twin,
		CreatedAt:     r.CreatedAt,
		LatestAttempt: r.RunAttempt,
		Status:        r.Status,
		Conclusion:    r.Conclusion,
		Attempts:      map[string]*attemptRecord{},
	}
}

// fireAndCorrelate dispatches n times (interval apart when non-zero), then
// waits until n runs beyond the known set appear and claims them.
func (d *dispatcher) fireAndCorrelate(ctx context.Context, workflow string, twin bool, n int, interval time.Duration) ([]ghRun, error) {
	known, err := d.knownRunIDs(ctx, workflow)
	if err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		if i > 0 && interval > 0 {
			if err := sleepCtx(ctx, interval); err != nil {
				return nil, err
			}
		}
		if err := d.dispatchOne(ctx, workflow, twin); err != nil {
			return nil, err
		}
	}
	runs, err := d.awaitNewRuns(ctx, workflow, known, n)
	for _, r := range runs {
		d.claimRun(r, workflow, twin)
	}
	return runs, err
}

// churn dispatches serially so each new run is unambiguously the one just
// fired, then cancels it at a random point in flight and re-runs it. The
// cancel/rerun tail runs concurrently per run; the next dispatch only waits
// for run correlation.
func (d *dispatcher) churn(ctx context.Context) error {
	known, err := d.knownRunIDs(ctx, d.cfg.workflow)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for i := 0; i < d.cfg.n; i++ {
		if err := d.dispatchOne(ctx, d.cfg.workflow, false); err != nil {
			wg.Wait()
			return err
		}
		runs, err := d.awaitNewRuns(ctx, d.cfg.workflow, known, 1)
		if err != nil {
			wg.Wait()
			return err
		}
		run := runs[0]
		d.claimRun(run, d.cfg.workflow, false)
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
		// 409 means the cancel lost the race to natural completion — a valid
		// churn outcome: nothing to rerun, nothing to assert. Anything else
		// (bad credential, missing scope, outage) must fail the pattern.
		var se *ghStatusError
		if errors.As(err, &se) && se.status == http.StatusConflict {
			return rec, nil
		}
		return rec, fmt.Errorf("cancel run %d: %w", runID, err)
	}
	rec.CancelConfirmed = true
	if _, err := d.awaitRunTerminal(ctx, runID); err != nil {
		return rec, err
	}
	if err := d.gh.rerunRun(ctx, d.cfg.repo, runID); err != nil {
		return rec, fmt.Errorf("rerun run %d: %w", runID, err)
	}
	now := time.Now().UTC()
	rec.RerunAt = &now
	return rec, nil
}

// awaitNewRuns polls the workflow's run list until want runs beyond the known
// set have appeared. workflow_dispatch returns no run id, so appearance is
// the only correlation there is — which is why churn dispatches serially.
// Runs that did appear are returned alongside a timeout error, so a partially
// correlated pattern is still recorded.
func (d *dispatcher) awaitNewRuns(ctx context.Context, workflow string, known map[int64]bool, want int) ([]ghRun, error) {
	since := d.st.StartedAt.Add(-2 * time.Minute)
	deadline := time.Now().Add(d.awaitTimeout)
	var got []ghRun
	for {
		runs, err := d.gh.listWorkflowRuns(ctx, d.cfg.repo, workflow, since)
		if err != nil {
			return got, err
		}
		for _, r := range runs {
			if known[r.ID] {
				continue
			}
			known[r.ID] = true
			got = append(got, r)
		}
		if len(got) >= want {
			return got, nil
		}
		if time.Now().After(deadline) {
			return got, fmt.Errorf("only %d of %d dispatched runs appeared in %s's run list", len(got), want, workflow)
		}
		if err := sleepCtx(ctx, d.pollInterval); err != nil {
			return got, err
		}
	}
}

func (d *dispatcher) awaitRunTerminal(ctx context.Context, runID int64) (ghRun, error) {
	deadline := time.Now().Add(d.awaitTimeout)
	for {
		run, err := d.gh.getRun(ctx, d.cfg.repo, runID)
		if err == nil && run.Status == "completed" {
			return run, nil
		}
		if time.Now().After(deadline) {
			return ghRun{}, fmt.Errorf("run %d never reached a terminal status", runID)
		}
		if err := sleepCtx(ctx, d.pollInterval); err != nil {
			return ghRun{}, err
		}
	}
}

func (d *dispatcher) awaitRunPromotion(ctx context.Context, runID int64) error {
	db, err := openDB(ctx, d.cfg.dbDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	deadline := time.Now().Add(d.awaitTimeout)
	for {
		promoted, detail, err := runPromoted(ctx, db, d.st.StartedAt.Add(-2*time.Minute), runID)
		if err != nil {
			return err
		}
		if promoted {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("run %d did not promote a generation: %s", runID, detail)
		}
		if err := sleepCtx(ctx, d.pollInterval); err != nil {
			return err
		}
	}
}

func runPromoted(ctx context.Context, db *dbClient, since time.Time, runID int64) (bool, string, error) {
	demands, err := db.DemandsSince(ctx, since)
	if err != nil {
		return false, "", err
	}
	var jobIDs []int64
	for _, demand := range demands {
		if demand.ProviderRunID == runID {
			jobIDs = append(jobIDs, demand.ProviderJobID)
		}
	}
	if len(jobIDs) == 0 {
		return false, "no provider demand observed", nil
	}

	leases, err := db.LeasesSince(ctx, since)
	if err != nil {
		return false, "", err
	}
	byJob := make(map[int64]leaseRow, len(leases))
	for _, lease := range leases {
		byJob[lease.ProviderJobID] = lease
	}
	scopes, err := db.Scopes(ctx)
	if err != nil {
		return false, "", err
	}
	current := map[string]bool{}
	for _, scope := range scopes {
		current[scope.CurrentGeneration] = true
	}
	generations, err := db.Generations(ctx)
	if err != nil {
		return false, "", err
	}
	states := map[string]string{}
	for _, generation := range generations {
		states[generation.Generation] = generation.State
	}

	for _, jobID := range jobIDs {
		lease, ok := byJob[jobID]
		if !ok {
			return false, fmt.Sprintf("job %d has no lease", jobID), nil
		}
		if lease.State != "completed" || lease.ReportedState != "sealed" {
			return false, fmt.Sprintf("lease %s is %s/%s", lease.LeaseID, lease.State, lease.ReportedState), nil
		}
		if lease.SealGeneration == "" {
			return false, fmt.Sprintf("lease %s has no seal generation", lease.LeaseID), nil
		}
		if states[lease.SealGeneration] != "committed" {
			return false, fmt.Sprintf("generation %s is %q", lease.SealGeneration, states[lease.SealGeneration]), nil
		}
		if !current[lease.SealGeneration] {
			return false, fmt.Sprintf("generation %s is not current", lease.SealGeneration), nil
		}
	}
	return true, "", nil
}

// restart fires a burst, waits for the load to be visibly in flight, then
// runs the operator-supplied restart command exactly once.
func (d *dispatcher) restart(ctx context.Context) error {
	runs, err := d.fireAndCorrelate(ctx, d.cfg.workflow, false, d.cfg.n, 0)
	if err != nil {
		return err
	}
	if d.cfg.restartCmd == "" {
		fmt.Fprintln(os.Stderr, "hammer: HAMMER_RESTART_CMD is unset; restart pattern dispatched load but skipped the restart")
		return nil
	}
	if err := d.awaitInFlight(ctx, (d.cfg.n+1)/2, runs); err != nil {
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

// awaitInFlight returns once enough of the pattern's runs are concurrently
// in_progress for the restart to land mid-flight — or, when the load is
// already draining, at the last moment something is still running. All runs
// completing first means the scenario was never exercised, which is a
// failure, not a quiet degrade to restart-at-idle.
func (d *dispatcher) awaitInFlight(ctx context.Context, want int, runs []ghRun) error {
	ids := map[int64]bool{}
	for _, r := range runs {
		ids[r.ID] = true
	}
	since := d.st.StartedAt.Add(-2 * time.Minute)
	deadline := time.Now().Add(d.awaitTimeout)
	for {
		list, err := d.gh.listWorkflowRuns(ctx, d.cfg.repo, d.cfg.workflow, since)
		if err != nil {
			return err
		}
		inFlight, completed := 0, 0
		for _, r := range list {
			if !ids[r.ID] {
				continue
			}
			switch r.Status {
			case "in_progress":
				inFlight++
			case "completed":
				completed++
			}
		}
		if inFlight >= want || (inFlight > 0 && inFlight+completed == len(ids)) {
			return nil
		}
		if completed == len(ids) {
			return fmt.Errorf("all %d runs completed before the restart could fire mid-flight", len(ids))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("only %d of %d runs in flight before the restart window closed", inFlight, want)
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
	if d.cfg.pattern == "serial" {
		return d.serial(ctx, d.cfg.twinWorkflow, true, d.cfg.twinN, false)
	}
	_, err := d.fireAndCorrelate(ctx, d.cfg.twinWorkflow, true, d.cfg.twinN, 0)
	return err
}
