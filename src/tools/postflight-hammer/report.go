package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// readyDeadlineBound is the current cancellation-propagation contract: a
// churned job must be terminal within hostd's ready/exited state deadline
// (see hostd/agent stateDeadlines). Tightening the contract later is a
// one-line change here.
const readyDeadlineBound = 30 * time.Minute

type assertionResult struct {
	Name    string `json:"name"`
	Pass    bool   `json:"pass"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type segmentStats struct {
	Name    string  `json:"name"`
	Samples int     `json:"samples"`
	P50ms   float64 `json:"p50_ms"`
	P90ms   float64 `json:"p90_ms"`
	P100ms  float64 `json:"p100_ms"`
	// Observed marks poll-resolution watch observations (true) versus
	// timestamps GitHub or the database recorded (false).
	Observed bool `json:"observed"`
}

type execStats struct {
	OurP50ms   float64 `json:"our_p50_ms"`
	OurP90ms   float64 `json:"our_p90_ms"`
	OurP100ms  float64 `json:"our_p100_ms"`
	OurSamples int     `json:"our_samples"`
	TwinP50ms  float64 `json:"twin_p50_ms"`
	TwinP90ms  float64 `json:"twin_p90_ms"`
	TwinP100ms float64 `json:"twin_p100_ms"`
	TwinCount  int     `json:"twin_samples"`
}

type generationStat struct {
	Generation string `json:"generation"`
	State      string `json:"state"`
	Bytes      int64  `json:"bytes"`
	DeltaBytes int64  `json:"delta_bytes"`
}

type scopeGrowth struct {
	Scope       string           `json:"scope"`
	Generations []generationStat `json:"generations"`
}

type report struct {
	GeneratedAt    time.Time         `json:"generated_at"`
	Repo           string            `json:"repo"`
	Workflow       string            `json:"workflow"`
	TwinWorkflow   string            `json:"twin_workflow,omitempty"`
	Dispatched     int               `json:"dispatched"`
	TwinDispatched int               `json:"twin_dispatched"`
	RunsObserved   int               `json:"runs_observed"`
	ChurnCycles    int               `json:"churn_cycles"`
	Assertions     []assertionResult `json:"assertions"`
	Pickup         []segmentStats    `json:"pickup"`
	Exec           *execStats        `json:"exec,omitempty"`
	Seal           *segmentStats     `json:"seal,omitempty"`
	NVMe           []scopeGrowth     `json:"nvme"`
	Pass           bool              `json:"pass"`
}

func buildReport(st *stateFile, now time.Time) *report {
	r := &report{
		GeneratedAt:  now.UTC(),
		Repo:         st.Repo,
		Workflow:     st.Workflow,
		TwinWorkflow: st.TwinWorkflow,
		ChurnCycles:  len(st.Churn),
	}
	for _, d := range st.Dispatches {
		if d.Twin {
			r.TwinDispatched++
		} else {
			r.Dispatched++
		}
	}
	r.RunsObserved = len(st.Runs)

	r.Assertions = []assertionResult{
		assertWatchCompleted(st),
		assertRunsGreenWithLogs(st),
		assertSlotBaseline(st),
		assertNoLeaks(st),
		assertChurnDeadlines(st),
		assertWarmDelta(st),
		assertGenerationInvariants(st),
	}
	r.Pickup = measurePickup(st)
	r.Exec = measureExec(st)
	r.Seal = measureSeal(st)
	r.NVMe = measureNVMe(st)

	r.Pass = true
	proved := false
	for i, a := range r.Assertions {
		if !a.Pass && !a.Skipped {
			r.Pass = false
		}
		if i > 0 && !a.Skipped {
			proved = true
		}
	}
	// A scoreboard where every assertion skipped proved nothing and must not
	// print a green standing-health verdict.
	if !proved {
		r.Pass = false
	}
	return r
}

// Assertion 0: the state file holds a finished watch. Reporting on a battery
// whose watch timed out, was interrupted, or never ran would grade partial
// observations — the skips that follow would read as a pass.
func assertWatchCompleted(st *stateFile) assertionResult {
	res := assertionResult{Name: "watch ran to completion"}
	if st.WatchDoneAt == nil {
		res.Detail = "no watch completed on this battery (timed out, interrupted, or never ran)"
		return res
	}
	res.Pass = true
	res.Detail = "watch finalized at " + st.WatchDoneAt.Format(time.RFC3339)
	return res
}

// cancelledAttempts returns the set of "runID/attempt" the churn pattern
// cancelled on purpose; those attempts are exempt from the success assertion.
func cancelledAttempts(st *stateFile) map[string]bool {
	out := map[string]bool{}
	for _, c := range st.Churn {
		if c.CancelConfirmed {
			out[fmt.Sprintf("%d/%d", c.RunID, c.CancelAttempt)] = true
		}
	}
	return out
}

func stepLogBytes(a *attemptRecord, jobName string, step int64) (int64, bool) {
	prefix := fmt.Sprintf("%s/%d_", jobName, step)
	for name, size := range a.StepLogBytes {
		if strings.HasPrefix(name, prefix) {
			return size, true
		}
	}
	return 0, false
}

// Assertion 1: every dispatch produced exactly one run the battery claimed,
// every attempt the churn pattern did not cancel reaches success on GitHub,
// and every successful step's log in the attempt archive is non-empty.
func assertRunsGreenWithLogs(st *stateFile) assertionResult {
	res := assertionResult{Name: "runs green with full per-step logs", Pass: true}
	exempt := cancelledAttempts(st)
	var problems []string
	dispatched, twinDispatched := 0, 0
	for _, d := range st.Dispatches {
		if d.Twin {
			twinDispatched++
		} else {
			dispatched++
		}
	}
	runs, twinRuns := 0, 0
	for _, run := range st.Runs {
		if run.Twin {
			twinRuns++
		} else {
			runs++
		}
	}
	if runs != dispatched {
		problems = append(problems, fmt.Sprintf("%d dispatches but %d runs claimed", dispatched, runs))
	}
	if twinRuns != twinDispatched {
		problems = append(problems, fmt.Sprintf("%d twin dispatches but %d twin runs claimed", twinDispatched, twinRuns))
	}
	attempts := 0
	for _, run := range st.Runs {
		if run.Twin {
			continue
		}
		for _, a := range run.Attempts {
			key := fmt.Sprintf("%d/%d", run.RunID, a.Attempt)
			if exempt[key] {
				continue
			}
			if a.Conclusion == "cancelled" {
				problems = append(problems, fmt.Sprintf("run %d attempt %d: cancelled outside the battery's churn cycles", run.RunID, a.Attempt))
				continue
			}
			attempts++
			if !a.terminal() || a.Conclusion != "success" {
				problems = append(problems, fmt.Sprintf("run %d attempt %d: %s/%s", run.RunID, a.Attempt, a.Status, a.Conclusion))
				continue
			}
			if !a.LogsFetched {
				problems = append(problems, fmt.Sprintf("run %d attempt %d: logs never fetched", run.RunID, a.Attempt))
				continue
			}
			for _, j := range a.Jobs {
				for _, s := range j.Steps {
					if s.Conclusion != "success" {
						continue
					}
					size, found := stepLogBytes(a, j.Name, s.Number)
					if !found || size == 0 {
						problems = append(problems, fmt.Sprintf("run %d attempt %d job %q step %d (%s): empty or missing log",
							run.RunID, a.Attempt, j.Name, s.Number, s.Name))
					}
				}
			}
		}
	}
	if attempts == 0 && len(problems) == 0 {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no non-cancelled attempts observed"}
	}
	if len(problems) > 0 {
		res.Pass = false
		res.Detail = summarize(problems)
	} else {
		res.Detail = fmt.Sprintf("%d attempts green, every successful step's log non-empty", attempts)
	}
	return res
}

// Assertion 2: slot accounting returns exactly to the pre-battery baseline.
func assertSlotBaseline(st *stateFile) assertionResult {
	res := assertionResult{Name: "slot accounting back to baseline", Pass: true}
	if st.Baseline == nil || st.DB == nil || st.DB.Final == nil {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no database baseline/final snapshot (DATABASE_URL unset?)"}
	}
	key := func(s slotRow) string { return s.HostID + "/" + s.Class }
	base := map[string]slotRow{}
	for _, s := range st.Baseline.Slots {
		base[key(s)] = s
	}
	final := map[string]slotRow{}
	for _, s := range st.DB.Final.Slots {
		final[key(s)] = s
	}
	var problems []string
	for k, b := range base {
		f, ok := final[k]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: slot row disappeared", k))
			continue
		}
		if f != b {
			problems = append(problems, fmt.Sprintf("%s: baseline total=%d warm=%d used=%d reserved=%d, final total=%d warm=%d used=%d reserved=%d",
				k, b.Total, b.Warm, b.Used, b.Reserved, f.Total, f.Warm, f.Used, f.Reserved))
		}
	}
	for k := range final {
		if _, ok := base[k]; !ok {
			problems = append(problems, fmt.Sprintf("%s: slot row appeared mid-battery", k))
		}
	}
	if len(problems) > 0 {
		res.Pass = false
		res.Detail = summarize(problems)
	} else {
		res.Detail = fmt.Sprintf("%d slot rows identical to baseline", len(base))
	}
	return res
}

// Assertion 3: nothing leaked — no demand or lease is still non-terminal past
// the deadline horizon. Host-disk orphans (zvols, VM state dirs) have no
// database-visible signal yet; they are covered by hostd's own GC and
// simulation tests.
func assertNoLeaks(st *stateFile) assertionResult {
	res := assertionResult{Name: "no leaks beyond deadline horizon", Pass: true}
	if st.DB == nil || st.DB.Final == nil {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no database observations (DATABASE_URL unset?)"}
	}
	horizon := st.DB.Final.CapturedAt.Add(-readyDeadlineBound)
	var problems []string
	for _, d := range st.DB.Demands {
		if !terminalDemandStates[d.State] && d.CreatedAt.Before(horizon) {
			problems = append(problems, fmt.Sprintf("demand job=%d stuck in %s since %s", d.ProviderJobID, d.State, d.CreatedAt.Format(time.RFC3339)))
		}
	}
	for _, l := range st.DB.Leases {
		if !terminalLeaseStates[l.State] && l.CreatedAt.Before(horizon) {
			problems = append(problems, fmt.Sprintf("lease %s stuck in %s since %s", l.LeaseID, l.State, l.CreatedAt.Format(time.RFC3339)))
		}
	}
	if len(problems) > 0 {
		res.Pass = false
		res.Detail = summarize(problems)
	} else {
		res.Detail = fmt.Sprintf("%d demands, %d leases clean", len(st.DB.Demands), len(st.DB.Leases))
	}
	return res
}

// Assertion 4: every churn-cancelled job's demand reaches a terminal state
// within the current contract bound.
func assertChurnDeadlines(st *stateFile) assertionResult {
	res := assertionResult{Name: "churn terminal within deadline bound", Pass: true}
	if len(st.Churn) == 0 {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no churn cycles in this battery"}
	}
	if st.DB == nil {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no database observations (DATABASE_URL unset?)"}
	}
	var problems []string
	checked := 0
	for _, c := range st.Churn {
		if !c.CancelConfirmed {
			continue
		}
		for _, d := range st.DB.Demands {
			if d.ProviderRunID != c.RunID || d.RunAttempt != c.CancelAttempt {
				continue
			}
			checked++
			if !terminalDemandStates[d.State] {
				problems = append(problems, fmt.Sprintf("run %d job %d: demand still %s after cancel", c.RunID, d.ProviderJobID, d.State))
				continue
			}
			if lag := d.UpdatedAt.Sub(c.CancelledAt); lag > readyDeadlineBound {
				problems = append(problems, fmt.Sprintf("run %d job %d: terminal %s after cancel (bound %s)", c.RunID, d.ProviderJobID, lag.Round(time.Second), readyDeadlineBound))
			}
		}
	}
	if checked == 0 {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no demands matched the cancelled attempts (cancelled before demand?)"}
	}
	if len(problems) > 0 {
		res.Pass = false
		res.Detail = summarize(problems)
	} else {
		res.Detail = fmt.Sprintf("%d cancelled-job demands terminal within %s", checked, readyDeadlineBound)
	}
	return res
}

type scopedJob struct {
	job     jobRecord
	attempt *attemptRecord
	runID   int64
}

// jobScopeGroups approximates a workspace scope from the GitHub side:
// same workflow, same job name. Only successful jobs participate.
func jobScopeGroups(st *stateFile) map[string][]scopedJob {
	groups := map[string][]scopedJob{}
	for _, run := range st.Runs {
		if run.Twin {
			continue
		}
		for _, a := range run.Attempts {
			for _, j := range a.Jobs {
				if j.Conclusion != "success" {
					continue
				}
				key := run.Workflow + "::" + j.Name
				groups[key] = append(groups[key], scopedJob{job: j, attempt: a, runID: run.RunID})
			}
		}
	}
	for _, g := range groups {
		sort.Slice(g, func(i, k int) bool { return g[i].job.StartedAt.Before(g[k].job.StartedAt) })
	}
	return groups
}

func checkoutStepDuration(j jobRecord) (time.Duration, bool) {
	for _, s := range j.Steps {
		if strings.Contains(strings.ToLower(s.Name), "checkout") && s.StartedAt != nil && s.CompletedAt != nil {
			return s.CompletedAt.Sub(*s.StartedAt), true
		}
	}
	return 0, false
}

// Assertion 5: after the first green run of a scope, subsequent runs are
// warm — their leases clone a generation, and the checkout step gets no
// slower than the cold run's. Per-lease bundle-server byte accounting does
// not exist yet, so the timing half is the delta heuristic (see --help).
func assertWarmDelta(st *stateFile) assertionResult {
	res := assertionResult{Name: "warm runs delta-only", Pass: true}
	leaseByJob := map[int64]leaseRow{}
	if st.DB != nil {
		for _, l := range st.DB.Leases {
			prev, seen := leaseByJob[l.ProviderJobID]
			if !seen || l.CreatedAt.After(prev.CreatedAt) {
				leaseByJob[l.ProviderJobID] = l
			}
		}
	}
	var problems []string
	warmChecked := 0
	for scope, jobs := range jobScopeGroups(st) {
		if len(jobs) < 2 {
			continue
		}
		cold, warm := jobs[0], jobs[1:]
		for _, w := range warm {
			warmChecked++
			if st.DB != nil {
				l, ok := leaseByJob[w.job.JobID]
				if !ok {
					problems = append(problems, fmt.Sprintf("%s: job %d has no lease row", scope, w.job.JobID))
				} else if l.WorkspaceGeneration == "" {
					problems = append(problems, fmt.Sprintf("%s: job %d ran cold (lease %s cloned no generation)", scope, w.job.JobID, l.LeaseID))
				}
			}
		}
		coldDur, coldOK := checkoutStepDuration(cold.job)
		var warmDurs []time.Duration
		for _, w := range warm {
			if d, ok := checkoutStepDuration(w.job); ok {
				warmDurs = append(warmDurs, d)
			}
		}
		if coldOK && len(warmDurs) > 0 {
			med := percentile(warmDurs, 0.5)
			if med > coldDur {
				problems = append(problems, fmt.Sprintf("%s: warm checkout median %s exceeds cold %s", scope, med.Round(time.Millisecond), coldDur.Round(time.Millisecond)))
			}
		}
	}
	if warmChecked == 0 {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no scope had a second green run"}
	}
	if len(problems) > 0 {
		res.Pass = false
		res.Detail = summarize(problems)
	} else {
		res.Detail = fmt.Sprintf("%d warm runs cloned a generation and checked out no slower than cold", warmChecked)
	}
	return res
}

func generationScope(g generationRow) string {
	return g.HostID + "/" + g.RunnerClass
}

// Assertion 6: generation lifecycle invariants in the final catalog state —
// at most one current per scope, every candidate resolved within the horizon.
func assertGenerationInvariants(st *stateFile) assertionResult {
	res := assertionResult{Name: "generation lifecycle invariants", Pass: true}
	if st.DB == nil || st.DB.Final == nil {
		return assertionResult{Name: res.Name, Skipped: true, Detail: "no database observations (DATABASE_URL unset?)"}
	}
	final := st.DB.Final
	now := final.CapturedAt
	var problems []string

	currents := map[string][]string{}
	for _, g := range final.Generations {
		if g.State == "current" {
			scope := generationScope(g)
			currents[scope] = append(currents[scope], g.Generation)
		}
		if g.State == "candidate" && now.Sub(g.CreatedAt) > readyDeadlineBound {
			problems = append(problems, fmt.Sprintf("generation %s: candidate unresolved since %s", g.Generation, g.CreatedAt.Format(time.RFC3339)))
		}
	}
	for scope, gens := range currents {
		if len(gens) > 1 {
			problems = append(problems, fmt.Sprintf("scope %s has %d current generations: %s", scope, len(gens), strings.Join(gens, ", ")))
		}
	}
	if len(problems) > 0 {
		res.Pass = false
		res.Detail = summarize(problems)
	} else {
		res.Detail = fmt.Sprintf("%d generations consistent", len(final.Generations))
	}
	return res
}

func summarize(problems []string) string {
	const max = 5
	if len(problems) <= max {
		return strings.Join(problems, "; ")
	}
	return fmt.Sprintf("%s; ... %d more", strings.Join(problems[:max], "; "), len(problems)-max)
}

func percentile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, k int) bool { return sorted[i] < sorted[k] })
	rank := int(q*float64(len(sorted))+0.5) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func segmentFrom(name string, samples []time.Duration, observed bool) segmentStats {
	return segmentStats{
		Name:     name,
		Samples:  len(samples),
		P50ms:    ms(percentile(samples, 0.5)),
		P90ms:    ms(percentile(samples, 0.9)),
		P100ms:   ms(percentile(samples, 1.0)),
		Observed: observed,
	}
}

// measurePickup splits webhook-received -> job-in-progress by segment.
// Authoritative boundaries come from the delivery ledger, demand/lease
// created_at, and GitHub's started_at; the interior hostd boundaries are
// watch observations at poll resolution.
func measurePickup(st *stateFile) []segmentStats {
	if st.DB == nil {
		return nil
	}
	o := st.DB
	leaseByJob := map[int64]leaseRow{}
	for _, l := range o.Leases {
		prev, seen := leaseByJob[l.ProviderJobID]
		if !seen || l.CreatedAt.After(prev.CreatedAt) {
			leaseByJob[l.ProviderJobID] = l
		}
	}
	firstReported := map[string]time.Time{}
	for _, t := range o.Transitions {
		if t.Kind == "lease" && t.Field == "reported_state" {
			if _, seen := firstReported[t.ID]; !seen {
				firstReported[t.ID] = t.ObservedAt
			}
		}
	}
	var ingest, claim, jit, pickup, listening, assignment, total []time.Duration
	collect := func(dst *[]time.Duration, from, to time.Time) {
		if from.IsZero() || to.IsZero() {
			return
		}
		if d := to.Sub(from); d >= 0 {
			*dst = append(*dst, d)
		}
	}
	for _, run := range st.Runs {
		if run.Twin {
			continue
		}
		for _, a := range run.Attempts {
			for _, j := range a.Jobs {
				if j.Conclusion != "success" || j.StartedAt.IsZero() {
					continue
				}
				key := fmt.Sprintf("%d", j.JobID)
				recv := o.Deliveries[key]
				demand, hasDemand := o.Demands[key]
				lease, hasLease := leaseByJob[j.JobID]
				if hasDemand {
					collect(&ingest, recv, demand.CreatedAt)
				}
				if hasDemand && hasLease {
					collect(&claim, demand.CreatedAt, lease.CreatedAt)
				}
				if hasLease {
					assignedAt, _ := o.observedAt("lease", lease.LeaseID, "state", "assigned")
					readyAt, _ := o.observedAt("lease", lease.LeaseID, "state", "ready")
					pickupAt := firstReported[lease.LeaseID]
					collect(&jit, lease.CreatedAt, assignedAt)
					collect(&pickup, assignedAt, pickupAt)
					collect(&listening, pickupAt, readyAt)
					collect(&assignment, readyAt, j.StartedAt)
				}
				collect(&total, recv, j.StartedAt)
			}
		}
	}
	return []segmentStats{
		segmentFrom("ingest (webhook -> demand)", ingest, false),
		segmentFrom("claim (demand -> lease)", claim, false),
		segmentFrom("jit (lease -> assigned)", jit, true),
		segmentFrom("hostd pickup (assigned -> reported)", pickup, true),
		segmentFrom("runner listening (reported -> ready)", listening, true),
		segmentFrom("github assignment (ready -> in_progress)", assignment, true),
		segmentFrom("total (webhook -> in_progress)", total, false),
	}
}

func measureExec(st *stateFile) *execStats {
	var ours, twins []time.Duration
	for _, run := range st.Runs {
		for _, a := range run.Attempts {
			for _, j := range a.Jobs {
				if j.Conclusion != "success" || j.StartedAt.IsZero() || j.CompletedAt.IsZero() {
					continue
				}
				d := j.CompletedAt.Sub(j.StartedAt)
				if run.Twin {
					twins = append(twins, d)
				} else {
					ours = append(ours, d)
				}
			}
		}
	}
	if len(ours) == 0 && len(twins) == 0 {
		return nil
	}
	return &execStats{
		OurP50ms: ms(percentile(ours, 0.5)), OurP90ms: ms(percentile(ours, 0.9)), OurP100ms: ms(percentile(ours, 1.0)), OurSamples: len(ours),
		TwinP50ms: ms(percentile(twins, 0.5)), TwinP90ms: ms(percentile(twins, 0.9)), TwinP100ms: ms(percentile(twins, 1.0)), TwinCount: len(twins),
	}
}

// measureSeal: runner exit to seal completion, from watch's observations of
// hostd's reported lease states.
func measureSeal(st *stateFile) *segmentStats {
	if st.DB == nil {
		return nil
	}
	var samples []time.Duration
	for _, l := range st.DB.Leases {
		exitedAt, ok := st.DB.observedAt("lease", l.LeaseID, "reported_state", "exited")
		if !ok {
			continue
		}
		sealedAt, ok := st.DB.observedAt("lease", l.LeaseID, "reported_state", "sealed")
		if !ok {
			continue
		}
		if d := sealedAt.Sub(exitedAt); d >= 0 {
			samples = append(samples, d)
		}
	}
	if len(samples) == 0 {
		return nil
	}
	s := segmentFrom("seal (exited -> sealed)", samples, true)
	return &s
}

// measureNVMe tabulates per-generation size statistics grouped by scope, in
// seal order, with each generation's growth over its predecessor.
func measureNVMe(st *stateFile) []scopeGrowth {
	if st.DB == nil || st.DB.Final == nil {
		return nil
	}
	byScope := map[string][]generationRow{}
	for _, g := range st.DB.Final.Generations {
		scope := generationScope(g)
		byScope[scope] = append(byScope[scope], g)
	}
	scopes := make([]string, 0, len(byScope))
	for s := range byScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	var out []scopeGrowth
	for _, scope := range scopes {
		gens := byScope[scope]
		sort.Slice(gens, func(i, k int) bool { return gens[i].CreatedAt.Before(gens[k].CreatedAt) })
		growth := scopeGrowth{Scope: scope}
		var prev int64
		for i, g := range gens {
			stat := generationStat{
				Generation: g.Generation,
				State:      g.State,
				Bytes:      g.Bytes,
			}
			if i > 0 {
				stat.DeltaBytes = g.Bytes - prev
			}
			prev = g.Bytes
			growth.Generations = append(growth.Generations, stat)
		}
		out = append(out, growth)
	}
	return out
}

func printReport(w io.Writer, r *report) {
	fmt.Fprintf(w, "postflight-hammer report — %s %s\n", r.Repo, r.Workflow)
	fmt.Fprintf(w, "dispatched %d (+%d twin), observed %d runs, %d churn cycles\n\n", r.Dispatched, r.TwinDispatched, r.RunsObserved, r.ChurnCycles)

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ASSERTION\tRESULT\tDETAIL")
	for _, a := range r.Assertions {
		verdict := "PASS"
		if a.Skipped {
			verdict = "SKIP"
		} else if !a.Pass {
			verdict = "FAIL"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Name, verdict, a.Detail)
	}
	tw.Flush()

	if len(r.Pickup) > 0 {
		fmt.Fprintf(w, "\npickup segments (ms; * = watch-observed, poll resolution)\n")
		tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SEGMENT\tN\tP50\tP90\tP100")
		for _, s := range r.Pickup {
			mark := ""
			if s.Observed {
				mark = " *"
			}
			fmt.Fprintf(tw, "%s%s\t%d\t%.0f\t%.0f\t%.0f\n", s.Name, mark, s.Samples, s.P50ms, s.P90ms, s.P100ms)
		}
		tw.Flush()
	}

	if r.Exec != nil {
		fmt.Fprintf(w, "\nexec: ours p50 %.0fms p90 %.0fms p100 %.0fms (n=%d)", r.Exec.OurP50ms, r.Exec.OurP90ms, r.Exec.OurP100ms, r.Exec.OurSamples)
		if r.Exec.TwinCount > 0 {
			fmt.Fprintf(w, "; twin p50 %.0fms p90 %.0fms p100 %.0fms (n=%d)", r.Exec.TwinP50ms, r.Exec.TwinP90ms, r.Exec.TwinP100ms, r.Exec.TwinCount)
		}
		fmt.Fprintln(w)
	}
	if r.Seal != nil {
		fmt.Fprintf(w, "seal: p50 %.0fms p90 %.0fms p100 %.0fms (n=%d, watch-observed)\n", r.Seal.P50ms, r.Seal.P90ms, r.Seal.P100ms, r.Seal.Samples)
	}

	if len(r.NVMe) > 0 {
		fmt.Fprintf(w, "\nNVMe growth per scope\n")
		tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SCOPE\tGENERATION\tSTATE\tBYTES\tDELTA")
		for _, sg := range r.NVMe {
			for _, g := range sg.Generations {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%+d\n", sg.Scope, g.Generation, g.State, g.Bytes, g.DeltaBytes)
			}
		}
		tw.Flush()
	}

	verdict := "PASS"
	if !r.Pass {
		verdict = "FAIL"
	}
	fmt.Fprintf(w, "\noverall: %s\n", verdict)
}

func runReport(statePath, jsonPath string) error {
	unlock, err := lockState(statePath)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := loadState(statePath)
	if err != nil {
		return err
	}
	r := buildReport(st, time.Now())
	printReport(os.Stdout, r)
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "json: %s\n", jsonPath)
	if !r.Pass {
		return fmt.Errorf("assertions failed")
	}
	return nil
}
