package main

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }
func tp(d time.Duration) *time.Time {
	t := at(d)
	return &t
}

func greenJob(jobID int64, start, checkoutDur time.Duration) jobRecord {
	return jobRecord{
		JobID: jobID, Name: "build", Status: "completed", Conclusion: "success",
		CreatedAt: at(0), StartedAt: at(start), CompletedAt: at(start + 25*time.Second),
		Steps: []stepRecord{
			{Number: 1, Name: "Set up job", Status: "completed", Conclusion: "success", StartedAt: tp(start), CompletedAt: tp(start + time.Second)},
			{Number: 2, Name: "Postflight checkout", Status: "completed", Conclusion: "success", StartedAt: tp(start + time.Second), CompletedAt: tp(start + time.Second + checkoutDur)},
			{Number: 3, Name: "Build and test", Status: "completed", Conclusion: "success", StartedAt: tp(start + 3*time.Second), CompletedAt: tp(start + 25*time.Second)},
		},
	}
}

func greenLogs() map[string]int64 {
	return map[string]int64{
		"build/1_Set up job.txt":          100,
		"build/2_Postflight checkout.txt": 50,
		"build/3_Build and test.txt":      500,
	}
}

func greenState() *stateFile {
	slots := []slotRow{{HostID: "h1", Class: "c4", Total: 4, Warm: 4, Used: 0, Reserved: 0}}
	done := at(3 * time.Minute)
	return &stateFile{
		Repo: "acme/demo", Workflow: "ci.yml", StartedAt: t0,
		Dispatches: []dispatchRecord{{Pattern: "burst", Workflow: "ci.yml", DispatchedAt: t0}},
		Runs: map[string]*runRecord{
			"500": {
				RunID: 500, Workflow: "ci.yml", CreatedAt: at(0), LatestAttempt: 1,
				Status: "completed", Conclusion: "success",
				Attempts: map[string]*attemptRecord{
					"1": {
						Attempt: 1, Status: "completed", Conclusion: "success", StartedAt: at(2 * time.Second),
						Jobs:        []jobRecord{greenJob(101, 9*time.Second, 2*time.Second)},
						LogsFetched: true, StepLogBytes: greenLogs(),
					},
				},
			},
		},
		Baseline: &dbSnapshot{CapturedAt: at(-time.Minute), Slots: slots},
		DB: &dbObservations{
			Demands: map[string]demandRow{
				"101": {ProviderJobID: 101, ProviderRunID: 500, RunAttempt: 1, State: "completed",
					CreatedAt: at(time.Second), UpdatedAt: at(35 * time.Second)},
			},
			Leases: map[string]leaseRow{
				"L1": {LeaseID: "L1", ProviderJobID: 101, State: "completed", ReportedState: "sealed",
					HostID: "h1", RunnerClass: "c4", SealGeneration: "gen-1",
					CreatedAt: at(2 * time.Second), UpdatedAt: at(40 * time.Second)},
			},
			Deliveries: map[string]time.Time{"101": at(500 * time.Millisecond)},
			Transitions: []transition{
				{Kind: "lease", ID: "L1", Field: "state", Value: "assigned", ObservedAt: at(3 * time.Second)},
				{Kind: "lease", ID: "L1", Field: "reported_state", Value: "claiming", ObservedAt: at(4 * time.Second)},
				{Kind: "lease", ID: "L1", Field: "state", Value: "ready", ObservedAt: at(6 * time.Second)},
				{Kind: "lease", ID: "L1", Field: "reported_state", Value: "exited", ObservedAt: at(35 * time.Second)},
				{Kind: "lease", ID: "L1", Field: "reported_state", Value: "sealed", ObservedAt: at(36 * time.Second)},
			},
			Final: &dbSnapshot{
				CapturedAt: at(2 * time.Minute),
				Slots:      slots,
				Scopes: []scopeRow{
					{ScopeID: "S1", Org: "acme", Repo: "demo", ScopeRef: "refs/heads/main",
						WorkflowPath: "ci.yml", JobName: "build", RunnerClass: "c4",
						CurrentGeneration: "gen-1", HomeHostID: "h1"},
				},
				Generations: []generationRow{
					{Generation: "gen-1", HostID: "h1", RunnerClass: "c4", State: "committed",
						Bytes: 1 << 30, CreatedAt: at(36 * time.Second), UpdatedAt: at(36 * time.Second)},
				},
			},
		},
		WatchDoneAt: &done,
	}
}

func assertionByName(t *testing.T, r *report, name string) assertionResult {
	t.Helper()
	for _, a := range r.Assertions {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no assertion %q", name)
	return assertionResult{}
}

func TestGreenBatteryPasses(t *testing.T) {
	r := buildReport(greenState(), at(4*time.Minute))
	if !r.Pass {
		t.Fatalf("green battery failed: %+v", r.Assertions)
	}
	for _, name := range []string{"watch ran to completion", "runs green with full per-step logs", "slot accounting back to baseline", "no leaks beyond deadline horizon", "generation lifecycle invariants"} {
		if a := assertionByName(t, r, name); !a.Pass || a.Skipped {
			t.Fatalf("%s: %+v", name, a)
		}
	}
	for _, name := range []string{"churn terminal within deadline bound", "warm runs delta-only"} {
		if a := assertionByName(t, r, name); !a.Skipped {
			t.Fatalf("%s should be skipped on this battery: %+v", name, a)
		}
	}
}

func TestReportFailsWithoutCompletedWatch(t *testing.T) {
	st := greenState()
	st.WatchDoneAt = nil
	r := buildReport(st, at(4*time.Minute))
	if r.Pass {
		t.Fatal("battery without a completed watch passed")
	}
	if a := assertionByName(t, r, "watch ran to completion"); a.Pass || a.Skipped {
		t.Fatalf("watch-completion assertion = %+v", a)
	}
}

func TestReportFailsWhenEveryAssertionSkips(t *testing.T) {
	done := at(time.Minute)
	st := &stateFile{
		Repo: "acme/demo", Workflow: "ci.yml", StartedAt: t0,
		Runs:        map[string]*runRecord{},
		WatchDoneAt: &done,
	}
	r := buildReport(st, at(4*time.Minute))
	if r.Pass {
		t.Fatal("battery that proved nothing passed")
	}
}

func TestRunsAssertionCatchesDispatchWithoutRun(t *testing.T) {
	st := greenState()
	st.Dispatches = append(st.Dispatches, dispatchRecord{Pattern: "burst", Workflow: "ci.yml", DispatchedAt: at(time.Second)})
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "runs green with full per-step logs")
	if a.Pass || a.Skipped {
		t.Fatalf("dispatch without a claimed run passed: %+v", a)
	}
	if !strings.Contains(a.Detail, "2 dispatches but 1 runs claimed") {
		t.Fatalf("detail does not name the mismatch: %s", a.Detail)
	}
}

func TestLogsAssertionCatchesForeignCancellation(t *testing.T) {
	st := greenState()
	st.Runs["500"].Attempts["1"].Conclusion = "cancelled"
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "runs green with full per-step logs")
	if a.Pass || a.Skipped {
		t.Fatalf("externally cancelled attempt passed: %+v", a)
	}
	if !strings.Contains(a.Detail, "outside the battery's churn cycles") {
		t.Fatalf("detail does not name the foreign cancel: %s", a.Detail)
	}
}

func TestLogsAssertionCatchesEmptyStepLog(t *testing.T) {
	st := greenState()
	st.Runs["500"].Attempts["1"].StepLogBytes["build/2_Postflight checkout.txt"] = 0
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "runs green with full per-step logs")
	if a.Pass || a.Skipped {
		t.Fatalf("empty step log passed: %+v", a)
	}
	if !strings.Contains(a.Detail, "step 2") {
		t.Fatalf("detail does not name the step: %s", a.Detail)
	}
}

func TestLogsAssertionCatchesFailedRun(t *testing.T) {
	st := greenState()
	st.Runs["500"].Attempts["1"].Conclusion = "failure"
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "runs green with full per-step logs")
	if a.Pass {
		t.Fatalf("failed run passed: %+v", a)
	}
}

func TestLogsAssertionExemptsChurnCancelledAttempt(t *testing.T) {
	st := greenState()
	st.Runs["500"].Attempts["1"].Conclusion = "cancelled"
	st.Churn = []churnRecord{{RunID: 500, CancelAttempt: 1, CancelConfirmed: true, CancelledAt: at(5 * time.Second)}}
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "runs green with full per-step logs")
	if !a.Skipped {
		t.Fatalf("only attempt was an intentional cancel; want skip, got %+v", a)
	}
}

func TestSlotAssertionCatchesDrift(t *testing.T) {
	st := greenState()
	st.DB.Final.Slots = []slotRow{{HostID: "h1", Class: "c4", Total: 4, Warm: 3, Used: 1, Reserved: 1}}
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "slot accounting back to baseline")
	if a.Pass || a.Skipped {
		t.Fatalf("slot drift passed: %+v", a)
	}
}

func TestLeakAssertionCatchesStuckDemand(t *testing.T) {
	st := greenState()
	st.DB.Final.CapturedAt = at(45 * time.Minute)
	st.DB.Demands["102"] = demandRow{ProviderJobID: 102, State: "demand_recorded", CreatedAt: at(0), UpdatedAt: at(0)}
	a := assertionByName(t, buildReport(st, at(46*time.Minute)), "no leaks beyond deadline horizon")
	if a.Pass {
		t.Fatalf("stuck demand passed: %+v", a)
	}
}

func TestChurnAssertionBoundsTerminalization(t *testing.T) {
	st := greenState()
	st.Churn = []churnRecord{{RunID: 500, CancelAttempt: 1, CancelConfirmed: true, CancelledAt: at(5 * time.Second)}}
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "churn terminal within deadline bound")
	if !a.Pass || a.Skipped {
		t.Fatalf("terminal-in-time churn should pass: %+v", a)
	}

	d := st.DB.Demands["101"]
	d.UpdatedAt = at(5*time.Second + readyDeadlineBound + time.Minute)
	st.DB.Demands["101"] = d
	a = assertionByName(t, buildReport(st, at(4*time.Minute)), "churn terminal within deadline bound")
	if a.Pass {
		t.Fatalf("late terminalization passed: %+v", a)
	}

	d.UpdatedAt = at(35 * time.Second)
	d.State = "capacity_requested"
	st.DB.Demands["101"] = d
	a = assertionByName(t, buildReport(st, at(4*time.Minute)), "churn terminal within deadline bound")
	if a.Pass {
		t.Fatalf("non-terminal demand passed: %+v", a)
	}
}

// warmState adds a second green run of the same scope whose lease cloned the
// generation the first run sealed.
func warmState() *stateFile {
	st := greenState()
	st.Runs["501"] = &runRecord{
		RunID: 501, Workflow: "ci.yml", CreatedAt: at(time.Minute), LatestAttempt: 1,
		Status: "completed", Conclusion: "success",
		Attempts: map[string]*attemptRecord{
			"1": {
				Attempt: 1, Status: "completed", Conclusion: "success", StartedAt: at(time.Minute),
				Jobs:        []jobRecord{greenJob(102, time.Minute+5*time.Second, time.Second)},
				LogsFetched: true, StepLogBytes: greenLogs(),
			},
		},
	}
	st.DB.Demands["102"] = demandRow{ProviderJobID: 102, ProviderRunID: 501, RunAttempt: 1, State: "completed",
		CreatedAt: at(time.Minute + time.Second), UpdatedAt: at(time.Minute + 40*time.Second)}
	st.DB.Leases["L2"] = leaseRow{LeaseID: "L2", ProviderJobID: 102, State: "completed", ReportedState: "sealed",
		HostID: "h1", RunnerClass: "c4", WorkspaceGeneration: "gen-1",
		CreatedAt: at(time.Minute + 2*time.Second), UpdatedAt: at(time.Minute + 40*time.Second)}
	return st
}

func TestWarmAssertionPassesOnClonedFasterRun(t *testing.T) {
	a := assertionByName(t, buildReport(warmState(), at(4*time.Minute)), "warm runs delta-only")
	if !a.Pass || a.Skipped {
		t.Fatalf("warm run should pass: %+v", a)
	}
}

func TestWarmAssertionCatchesColdSecondRun(t *testing.T) {
	st := warmState()
	l := st.DB.Leases["L2"]
	l.WorkspaceGeneration = ""
	st.DB.Leases["L2"] = l
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "warm runs delta-only")
	if a.Pass {
		t.Fatalf("uncloned warm run passed: %+v", a)
	}
}

func TestWarmAssertionCatchesSlowWarmCheckout(t *testing.T) {
	st := warmState()
	st.Runs["501"].Attempts["1"].Jobs = []jobRecord{greenJob(102, time.Minute+5*time.Second, 10*time.Second)}
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "warm runs delta-only")
	if a.Pass {
		t.Fatalf("slow warm checkout passed: %+v", a)
	}
}

func TestGenerationInvariantsCatchBrokenPointersAndStaleCandidate(t *testing.T) {
	st := greenState()
	st.DB.Final.Scopes[0].CurrentGeneration = "gen-gone"
	a := assertionByName(t, buildReport(st, at(4*time.Minute)), "generation lifecycle invariants")
	if a.Pass {
		t.Fatalf("pointer at a missing generation passed: %+v", a)
	}

	st = greenState()
	st.DB.Final.Generations[0].State = "retained"
	a = assertionByName(t, buildReport(st, at(4*time.Minute)), "generation lifecycle invariants")
	if a.Pass {
		t.Fatalf("pointer at a retained generation passed: %+v", a)
	}

	st = greenState()
	st.DB.Final.Generations = append(st.DB.Final.Generations, generationRow{
		Generation: "gen-2", HostID: "h1", RunnerClass: "c4", State: "committed",
		Bytes: 1 << 30, CreatedAt: at(time.Minute), UpdatedAt: at(time.Minute),
	})
	a = assertionByName(t, buildReport(st, at(4*time.Minute)), "generation lifecycle invariants")
	if a.Pass {
		t.Fatalf("committed generation without a scope pointer passed: %+v", a)
	}

	st = greenState()
	st.DB.Final.CapturedAt = at(45 * time.Minute)
	st.DB.Final.Generations = append(st.DB.Final.Generations, generationRow{
		Generation: "gen-2", HostID: "h1", RunnerClass: "c4", State: "candidate",
		CreatedAt: at(0), UpdatedAt: at(0),
	})
	a = assertionByName(t, buildReport(st, at(46*time.Minute)), "generation lifecycle invariants")
	if a.Pass {
		t.Fatalf("stale candidate passed: %+v", a)
	}
}

func TestMeasurePickupSegments(t *testing.T) {
	segments := measurePickup(greenState())
	want := map[string]float64{
		"ingest (webhook -> demand)":               500,
		"claim (demand -> lease)":                  1000,
		"jit (lease -> assigned)":                  1000,
		"hostd pickup (assigned -> reported)":      1000,
		"runner listening (reported -> ready)":     2000,
		"github assignment (ready -> in_progress)": 3000,
		"total (webhook -> in_progress)":           8500,
	}
	for _, s := range segments {
		expect, ok := want[s.Name]
		if !ok {
			t.Fatalf("unexpected segment %q", s.Name)
		}
		if s.Samples != 1 || s.P50ms != expect {
			t.Fatalf("%s: n=%d p50=%.0f, want n=1 p50=%.0f", s.Name, s.Samples, s.P50ms, expect)
		}
	}
}

func TestMeasureSealFromObservedTransitions(t *testing.T) {
	s := measureSeal(greenState())
	if s == nil || s.Samples != 1 || s.P50ms != 1000 {
		t.Fatalf("seal = %+v, want one 1000ms sample", s)
	}
}

func TestMeasureNVMeGrowth(t *testing.T) {
	st := greenState()
	st.DB.Final.Generations = append(st.DB.Final.Generations, generationRow{
		Generation: "gen-2", HostID: "h1", RunnerClass: "c4", State: "committed",
		Bytes: 3 << 29, CreatedAt: at(time.Minute), UpdatedAt: at(time.Minute),
	})
	growth := measureNVMe(st)
	if len(growth) != 1 || len(growth[0].Generations) != 2 {
		t.Fatalf("growth = %+v", growth)
	}
	if growth[0].Generations[1].DeltaBytes != 3<<29-1<<30 {
		t.Fatalf("delta = %d", growth[0].Generations[1].DeltaBytes)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	ds := []time.Duration{4, 1, 3, 2, 5}
	if got := percentile(ds, 0.5); got != 3 {
		t.Fatalf("p50 = %d", got)
	}
	if got := percentile(ds, 1.0); got != 5 {
		t.Fatalf("p100 = %d", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Fatalf("empty = %d", got)
	}
}
