package main

// Store-level proofs for the workspace generation lifecycle, against a real
// PostgreSQL: cold seed, warm clone, the promotion CAS under races, the
// PR-trust write ban, ambiguity never advancing the pointer, and the
// retention sweep never releasing a referenced generation. The randomized
// test at the bottom is the property-style check: invariants asserted after
// every round, with forced rounds proving each assertion non-vacuous.

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

func testScheduler(st *pgStore) *scheduler {
	return &scheduler{st: st, cfg: config{workerBatchSize: 16}, tracer: noop.NewTracerProvider().Tracer("test")}
}

func ensureScope(t *testing.T, st *pgStore, jobName string) string {
	t.Helper()
	scopeID, err := st.EnsureWorkspaceScope(context.Background(), workspaceScopeKey{
		Org: "acme", Repo: "widget", ScopeRef: "main",
		WorkflowPath: ".github/workflows/ci.yml", JobName: jobName,
		RunnerClass: storeTestClass,
	})
	if err != nil || scopeID == "" {
		t.Fatalf("ensure scope: id=%q err=%v", scopeID, err)
	}
	return scopeID
}

func seedTrustedDemand(t *testing.T, pool *pgxpool.Pool, jobID int64, trust, scopeID string) schedulableDemand {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO github_workflow_jobs (provider_job_id, provider_run_attempt, runner_class) VALUES ($1, 1, $2)`,
		jobID, storeTestClass); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO github_provider_demands (provider_job_id, repository_full_name, provider_run_attempt,
		     trust_class, runner_class, workspace_scope_id, state)
		 VALUES ($1, 'acme/widget', 1, $2, $3, NULLIF($4, '')::uuid, 'demand_recorded')`,
		jobID, trust, storeTestClass, scopeID); err != nil {
		t.Fatal(err)
	}
	return schedulableDemand{
		ProviderJobID: jobID, ProviderRepositoryID: 1, RepositoryFullName: "acme/widget",
		ProviderRunAttempt: 1, RunnerClass: storeTestClass, DiskBytes: 1 << 30, WorkspaceScopeID: scopeID,
	}
}

// placeAssignedLease drives a demand to an assigned lease on a claimed slot.
func placeAssignedLease(t *testing.T, st *pgStore, d schedulableDemand) (leaseID, hostID string) {
	t.Helper()
	ctx := context.Background()
	leaseID = mustCreateLease(t, st, d, time.Now().Add(time.Minute))
	hostID, claimed, err := st.ClaimHostSlot(ctx, leaseID, storeTestClass)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if assigned, err := st.AssignLease(ctx, leaseID, hostID, "jit-blob", time.Now().Add(time.Minute)); err != nil || !assigned {
		t.Fatalf("assign: assigned=%v err=%v", assigned, err)
	}
	return leaseID, hostID
}

// exitAndSeal reports exit 0 and the host's seal confirmation, returning the
// candidate generation.
func exitAndSeal(t *testing.T, st *pgStore, hostID, leaseID string) string {
	t.Helper()
	ctx := context.Background()
	_, generation, done, err := st.CompleteLease(ctx, hostID, leaseID, 0, time.Now().Add(time.Minute))
	if err != nil || !done || generation == "" {
		t.Fatalf("exit-0 branch-trust lease did not request a seal: generation=%q done=%v err=%v", generation, done, err)
	}
	if _, sealed, err := st.RecordLeaseSealed(ctx, hostID, leaseID, generation); err != nil || !sealed {
		t.Fatalf("record sealed: sealed=%v err=%v", sealed, err)
	}
	return generation
}

// observeJobConclusion writes GitHub's API-read verdict onto the job row.
func observeJobConclusion(t *testing.T, pool *pgxpool.Pool, jobID, attempt int64, conclusion string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE github_workflow_jobs SET status = 'completed', conclusion = $2,
		     provider_run_attempt = $3, observed_from_api_at = now(),
		     terminal_observed_from_api_at = now(), updated_at = now()
		 WHERE provider_job_id = $1`,
		jobID, conclusion, attempt); err != nil {
		t.Fatal(err)
	}
}

func scopePointer(t *testing.T, pool *pgxpool.Pool, scopeID string) (generation, homeHost string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(current_generation_id, ''), home_host_id FROM workspace_scopes WHERE scope_id = $1::uuid`,
		scopeID).Scan(&generation, &homeHost); err != nil {
		t.Fatal(err)
	}
	return generation, homeHost
}

func generationState(t *testing.T, pool *pgxpool.Pool, generation string) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(context.Background(),
		`SELECT state FROM workspace_generations WHERE generation = $1`, generation).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// assertPointerCommitted is generation invariant 1: the scope pointer never
// references a generation that is not committed.
func assertPointerCommitted(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var violations int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM workspace_scopes s
		 JOIN workspace_generations g ON g.generation = s.current_generation_id
		 WHERE g.state <> 'committed'`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if violations != 0 {
		t.Fatalf("scope pointer references %d non-committed generations", violations)
	}
}

// TestColdSeedWarmCloneAndRetirement is the full lifecycle: an empty scope
// seeds its lineage from the first green run, the next run clones the
// promoted generation, and the displaced generation retires through
// retained -> reapable -> reaped as references disappear.
func TestColdSeedWarmCloneAndRetirement(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 4)
	scopeID := ensureScope(t, st, "build")
	sched := testScheduler(st)

	// Cold: the scope has no pointer, so the claim materializes empty.
	lease1, host1 := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 101, trustClassBranch, scopeID))
	if got := leaseColumn(t, pool, lease1, "workspace_generation"); got != "" {
		t.Fatalf("cold claim stamped workspace_generation %q, want empty", got)
	}
	if got := leaseColumn(t, pool, lease1, "COALESCE(observed_source_generation, '<null>')"); got != "<null>" {
		t.Fatalf("cold claim observed_source = %q, want NULL", got)
	}

	// Exit 0 on branch trust: the lease holds in 'sealing' with a minted
	// candidate, the slot frees, and the demand completes without waiting
	// on GitHub.
	_, gen1, done, err := st.CompleteLease(ctx, host1, lease1, 0, time.Now().Add(time.Minute))
	if err != nil || !done || gen1 == "" {
		t.Fatalf("complete: generation=%q done=%v err=%v", gen1, done, err)
	}
	if got := leaseColumn(t, pool, lease1, "state"); got != leaseSealing {
		t.Fatalf("lease state = %q, want sealing", got)
	}
	if got := reservedCount(t, pool, "host-a"); got != 0 {
		t.Fatalf("reserved = %d after exited report, want 0 (occupancy never waits on the seal)", got)
	}
	if got := demandState(t, pool, 101); got != demandCompleted {
		t.Fatalf("demand state = %q, want completed", got)
	}

	// The seal request rides the desired set.
	desired, err := st.ListDesiredLeases(ctx, host1)
	if err != nil || len(desired) != 1 {
		t.Fatalf("desired = %+v err=%v, want exactly the sealing lease", desired, err)
	}
	if desired[0].State != leaseSealing || desired[0].SealGeneration != gen1 {
		t.Fatalf("desired projection = %+v, want a seal request for %s", desired[0], gen1)
	}

	if _, sealed, err := st.RecordLeaseSealed(ctx, host1, lease1, gen1); err != nil || !sealed {
		t.Fatalf("record sealed: sealed=%v err=%v", sealed, err)
	}
	if got := leaseColumn(t, pool, lease1, "state"); got != leaseCompleted {
		t.Fatalf("lease state after sealed report = %q, want completed", got)
	}

	// Not yet observed on GitHub: nothing advances.
	sched.promoteSealedGenerations(ctx)
	if pointer, _ := scopePointer(t, pool, scopeID); pointer != "" {
		t.Fatalf("pointer advanced to %q before GitHub's verdict was observed", pointer)
	}

	observeJobConclusion(t, pool, 101, 1, "success")
	sched.promoteSealedGenerations(ctx)
	pointer, homeHost := scopePointer(t, pool, scopeID)
	if pointer != gen1 || homeHost != host1 {
		t.Fatalf("pointer = %q home = %q, want %q on %q", pointer, homeHost, gen1, host1)
	}
	if got := generationState(t, pool, gen1); got != genCommitted {
		t.Fatalf("promoted generation state = %q, want committed", got)
	}
	assertPointerCommitted(t, pool)

	// Warm: the next lease of the scope clones the promoted generation.
	lease2, host2 := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 102, trustClassBranch, scopeID))
	if host2 != host1 {
		t.Fatalf("second lease placed on %q, want the home host %q", host2, host1)
	}
	if got := leaseColumn(t, pool, lease2, "workspace_generation"); got != gen1 {
		t.Fatalf("warm claim workspace_generation = %q, want %q", got, gen1)
	}
	if got := leaseColumn(t, pool, lease2, "observed_source_generation"); got != gen1 {
		t.Fatalf("warm claim observed_source = %q, want %q", got, gen1)
	}

	gen2 := exitAndSeal(t, st, host2, lease2)
	observeJobConclusion(t, pool, 102, 1, "success")
	sched.promoteSealedGenerations(ctx)
	if pointer, _ := scopePointer(t, pool, scopeID); pointer != gen2 {
		t.Fatalf("pointer = %q, want the successor %q", pointer, gen2)
	}
	if got := generationState(t, pool, gen1); got != genRetained {
		t.Fatalf("displaced generation state = %q, want retained", got)
	}
	assertPointerCommitted(t, pool)

	// Retirement: unreferenced retained generations release to the reap
	// dispatch, and the host's inventory omission confirms destruction.
	if _, err := st.SweepReapableGenerations(ctx); err != nil {
		t.Fatal(err)
	}
	if got := generationState(t, pool, gen1); got != genReapable {
		t.Fatalf("swept generation state = %q, want reapable", got)
	}
	reap, err := st.ListReapGenerations(ctx, host1)
	if err != nil || len(reap) != 1 || reap[0] != gen1 {
		t.Fatalf("reap dispatch = %v err=%v, want exactly [%s]", reap, err, gen1)
	}
	if err := st.ObserveHostGenerations(ctx, host1, []syncproto.GenerationReport{{Generation: gen2, Bytes: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := generationState(t, pool, gen1); got != genReaped {
		t.Fatalf("generation absent from inventory has state %q, want reaped", got)
	}
	if got := generationState(t, pool, gen2); got != genCommitted {
		t.Fatalf("current generation state = %q, want committed (still reported)", got)
	}
}

// TestPromotionCASRace: two leases cloned from the same observed source both
// go green — exactly one advances the pointer, the loser is retained, and
// the pointer never references a non-committed generation.
func TestPromotionCASRace(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 4)
	scopeID := ensureScope(t, st, "build")
	sched := testScheduler(st)

	leaseA, hostA := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 201, trustClassBranch, scopeID))
	leaseB, hostB := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 202, trustClassBranch, scopeID))
	genA := exitAndSeal(t, st, hostA, leaseA)
	genB := exitAndSeal(t, st, hostB, leaseB)
	observeJobConclusion(t, pool, 201, 1, "success")
	observeJobConclusion(t, pool, 202, 1, "success")

	// Two control-plane instances race the same promotion pass.
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sched.promoteSealedGenerations(ctx)
		}()
	}
	wg.Wait()

	pointer, _ := scopePointer(t, pool, scopeID)
	stateA, stateB := generationState(t, pool, genA), generationState(t, pool, genB)
	var winner, loser string
	switch pointer {
	case genA:
		winner, loser = stateA, stateB
	case genB:
		winner, loser = stateB, stateA
	default:
		t.Fatalf("pointer = %q, want one of the racing candidates", pointer)
	}
	if winner != genCommitted || loser != genRetained {
		t.Fatalf("winner=%s loser=%s, want committed/retained (genA=%s genB=%s)", winner, loser, stateA, stateB)
	}
	assertPointerCommitted(t, pool)
}

// TestPRTrustNeverSeals: a green run on anything but branch trust completes
// plainly — no seal request, no candidate, no pointer movement.
func TestPRTrustNeverSeals(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 4)
	scopeID := ensureScope(t, st, "build")

	jobID := int64(300)
	for _, trust := range []string{trustClassPR, trustClassPRFork, trustClassUnknown} {
		jobID++
		leaseID, hostID := placeAssignedLease(t, st, seedTrustedDemand(t, pool, jobID, trust, scopeID))
		_, generation, done, err := st.CompleteLease(ctx, hostID, leaseID, 0, time.Now().Add(time.Minute))
		if err != nil || !done {
			t.Fatalf("%s: complete: done=%v err=%v", trust, done, err)
		}
		if generation != "" {
			t.Fatalf("%s: exit-0 lease minted candidate %q, want no seal", trust, generation)
		}
		if got := leaseColumn(t, pool, leaseID, "state"); got != leaseCompleted {
			t.Fatalf("%s: lease state = %q, want completed", trust, got)
		}
	}

	var candidates int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM workspace_generations`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("%d generation rows exist after non-branch-trust runs, want 0", candidates)
	}
	if pointer, _ := scopePointer(t, pool, scopeID); pointer != "" {
		t.Fatalf("pointer = %q, want untouched", pointer)
	}
}

// TestFailedRunNeverSeals: a non-zero exit completes plainly even on branch
// trust.
func TestFailedRunNeverSeals(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 4)
	scopeID := ensureScope(t, st, "build")

	leaseID, hostID := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 401, trustClassBranch, scopeID))
	_, generation, done, err := st.CompleteLease(ctx, hostID, leaseID, 1, time.Now().Add(time.Minute))
	if err != nil || !done || generation != "" {
		t.Fatalf("exit-1 complete: generation=%q done=%v err=%v, want plain completion", generation, done, err)
	}
	if got := leaseColumn(t, pool, leaseID, "state"); got != leaseCompleted {
		t.Fatalf("lease state = %q, want completed", got)
	}
}

// TestAmbiguityNeverAdvances: every verdict that is not an attempt-matching
// API-observed success discards (or holds) the candidate; the previous
// current stays authoritative throughout.
func TestAmbiguityNeverAdvances(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 8)
	scopeID := ensureScope(t, st, "build")
	sched := testScheduler(st)

	// Seed the lineage so "stays authoritative" is observable.
	leaseSeed, hostSeed := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 500, trustClassBranch, scopeID))
	genSeed := exitAndSeal(t, st, hostSeed, leaseSeed)
	observeJobConclusion(t, pool, 500, 1, "success")
	sched.promoteSealedGenerations(ctx)
	if pointer, _ := scopePointer(t, pool, scopeID); pointer != genSeed {
		t.Fatalf("seed promotion failed: pointer = %q", pointer)
	}

	cases := []struct {
		jobID      int64
		observe    func(gen string)
		wantState  string
		wantReason string
	}{
		{501, func(string) {}, genCandidate, "verdict not yet observed"},
		{502, func(gen string) { observeJobConclusion(t, pool, 502, 1, "failure") }, genDiscarded, "failure conclusion"},
		{503, func(gen string) { observeJobConclusion(t, pool, 503, 1, "cancelled") }, genDiscarded, "cancelled conclusion"},
		{504, func(gen string) { observeJobConclusion(t, pool, 504, 2, "success") }, genDiscarded, "stale attempt"},
		{505, func(gen string) {
			// A completed webhook HINT over a sticky queued-time API read:
			// the row looks terminal but no API read carried the completed
			// status, so the candidate must hold.
			if _, err := pool.Exec(ctx,
				`UPDATE github_workflow_jobs SET status = 'completed', conclusion = 'success',
				     observed_from_api_at = now(), updated_at = now()
				 WHERE provider_job_id = 505`); err != nil {
				t.Fatal(err)
			}
		}, genCandidate, "hint-only completion"},
	}
	for _, tc := range cases {
		leaseID, hostID := placeAssignedLease(t, st, seedTrustedDemand(t, pool, tc.jobID, trustClassBranch, scopeID))
		gen := exitAndSeal(t, st, hostID, leaseID)
		tc.observe(gen)
		sched.promoteSealedGenerations(ctx)
		if got := generationState(t, pool, gen); got != tc.wantState {
			t.Errorf("%s: generation state = %q, want %q", tc.wantReason, got, tc.wantState)
		}
		if pointer, _ := scopePointer(t, pool, scopeID); pointer != genSeed {
			t.Errorf("%s: pointer = %q, want %q untouched", tc.wantReason, pointer, genSeed)
		}
	}
	assertPointerCommitted(t, pool)
}

// TestSealFailureAndExpiryDiscard: a host-reported failure mid-seal, or a
// seal that never confirms, discards the candidate without touching the
// demand's completed verdict or the slot accounting.
func TestSealFailureAndExpiryDiscard(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 4)
	scopeID := ensureScope(t, st, "build")

	// Host reports failed while sealing.
	leaseA, hostA := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 601, trustClassBranch, scopeID))
	_, genA, _, err := st.CompleteLease(ctx, hostA, leaseA, 0, time.Now().Add(time.Minute))
	if err != nil || genA == "" {
		t.Fatalf("complete: generation=%q err=%v", genA, err)
	}
	failed, err := st.FailSealingLease(ctx, hostA, leaseA, "failed", "seal: zfs snapshot failed")
	if err != nil || !failed {
		t.Fatalf("fail sealing: failed=%v err=%v", failed, err)
	}
	if got := generationState(t, pool, genA); got != genDiscarded {
		t.Fatalf("generation state after seal failure = %q, want discarded", got)
	}
	if got := demandState(t, pool, 601); got != demandCompleted {
		t.Fatalf("demand state = %q, want completed (a lost seal is never a job failure)", got)
	}

	// Seal deadline passes with no confirmation.
	leaseB, hostB := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 602, trustClassBranch, scopeID))
	_, genB, _, err := st.CompleteLease(ctx, hostB, leaseB, 0, time.Now().Add(-time.Second))
	if err != nil || genB == "" {
		t.Fatalf("complete: generation=%q err=%v", genB, err)
	}
	overdue, err := st.ListOverdueLeases(ctx, 16, time.Now().Add(-5*time.Minute))
	if err != nil || len(overdue) != 1 || overdue[0].State != leaseSealing {
		t.Fatalf("overdue = %+v err=%v, want the sealing lease", overdue, err)
	}
	before := reservedCount(t, pool, "host-a")
	if expired, err := st.ExpireLease(ctx, overdue[0], "seal not confirmed", nil); err != nil || !expired {
		t.Fatalf("expire sealing: expired=%v err=%v", expired, err)
	}
	if got := generationState(t, pool, genB); got != genDiscarded {
		t.Fatalf("generation state after seal expiry = %q, want discarded", got)
	}
	if got := reservedCount(t, pool, "host-a"); got != before {
		t.Fatalf("reserved = %d after seal expiry, want %d (slot was already released at exit)", got, before)
	}
	if got := demandState(t, pool, 602); got != demandCompleted {
		t.Fatalf("demand state = %q, want completed", got)
	}
}

// TestReapNeverSelectsReferencedGenerations proves each reference class
// blocks the sweep — the pointer, a pin, and a live lease — by releasing
// them one at a time and watching the generation become reapable only then.
func TestReapNeverSelectsReferencedGenerations(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 4)
	scopeID := ensureScope(t, st, "build")

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	seedGen := func(name, state string, pinned bool) {
		mustExec(`INSERT INTO workspace_generations (generation, host_id, runner_class, state, pinned)
		          VALUES ($1, 'host-a', $2, $3, $4)`, name, storeTestClass, state, pinned)
	}
	sweepAndAssert := func(generation, want, blockedBy string) {
		t.Helper()
		if _, err := st.SweepReapableGenerations(ctx); err != nil {
			t.Fatal(err)
		}
		if got := generationState(t, pool, generation); got != want {
			t.Fatalf("generation %s (blocked by %s): state = %q, want %q", generation, blockedBy, got, want)
		}
	}

	// Pointer-referenced (a state only reachable through direct injection —
	// promotions demote before repointing — but the sweep must still hold).
	seedGen("gen-pointer", genRetained, false)
	mustExec(`UPDATE workspace_scopes SET current_generation_id = 'gen-pointer' WHERE scope_id = $1::uuid`, scopeID)
	sweepAndAssert("gen-pointer", genRetained, "the scope pointer")
	mustExec(`UPDATE workspace_scopes SET current_generation_id = NULL WHERE scope_id = $1::uuid`, scopeID)
	sweepAndAssert("gen-pointer", genReapable, "nothing")

	// Pinned.
	seedGen("gen-pinned", genDiscarded, true)
	sweepAndAssert("gen-pinned", genDiscarded, "the pin")
	mustExec(`UPDATE workspace_generations SET pinned = false WHERE generation = 'gen-pinned'`)
	sweepAndAssert("gen-pinned", genReapable, "nothing")

	// Referenced by a live lease as its clone source.
	seedGen("gen-lease", genRetained, false)
	leaseID, hostID := placeAssignedLease(t, st, seedTrustedDemand(t, pool, 701, trustClassBranch, scopeID))
	mustExec(`UPDATE host_leases SET workspace_generation = 'gen-lease', observed_source_generation = 'gen-lease'
	          WHERE lease_id = $1`, leaseID)
	sweepAndAssert("gen-lease", genRetained, "a live lease")
	if _, _, done, err := st.CompleteLease(ctx, hostID, leaseID, 1, time.Now().Add(time.Minute)); err != nil || !done {
		t.Fatalf("complete: done=%v err=%v", done, err)
	}
	sweepAndAssert("gen-lease", genReapable, "nothing")

	// The reap dispatch never includes pinned rows even if forced reapable.
	mustExec(`UPDATE workspace_generations SET pinned = true WHERE generation = 'gen-pinned'`)
	reap, err := st.ListReapGenerations(ctx, "host-a")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"gen-pointer": true, "gen-lease": true}
	if len(reap) != len(want) {
		t.Fatalf("reap dispatch = %v, want exactly %v", reap, want)
	}
	for _, g := range reap {
		if !want[g] {
			t.Fatalf("reap dispatch = %v includes %q, want only %v", reap, g, want)
		}
	}
}

// TestPromotionInvariantsRandomized is the property-style check: random
// batches of racing candidates with random verdicts, promoted by two
// concurrent scheduler passes, with the lifecycle invariants asserted after
// every round. Round 0 forces multiple successes and round 1 forces all
// failures so every assertion below is demonstrably non-vacuous.
func TestPromotionInvariantsRandomized(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 8)
	scopeID := ensureScope(t, st, "build")
	sched := testScheduler(st)
	rng := rand.New(rand.NewSource(42))

	var promotedEver, retainedEver, discardedEver int
	jobID := int64(1000)
	for round := 0; round < 8; round++ {
		count := 1 + rng.Intn(3)
		success := make([]bool, count)
		for i := range success {
			success[i] = rng.Intn(2) == 0
		}
		switch round {
		case 0:
			count, success = 2, []bool{true, true}
		case 1:
			count, success = 2, []bool{false, false}
		}

		pointerBefore, _ := scopePointer(t, pool, scopeID)
		generations := make([]string, count)
		successes := 0
		for i := 0; i < count; i++ {
			jobID++
			leaseID, hostID := placeAssignedLease(t, st, seedTrustedDemand(t, pool, jobID, trustClassBranch, scopeID))
			generations[i] = exitAndSeal(t, st, hostID, leaseID)
			if success[i] {
				successes++
				observeJobConclusion(t, pool, jobID, 1, "success")
			} else {
				observeJobConclusion(t, pool, jobID, 1, "failure")
			}
		}

		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sched.promoteSealedGenerations(ctx)
			}()
		}
		wg.Wait()

		assertPointerCommitted(t, pool)
		pointerAfter, _ := scopePointer(t, pool, scopeID)
		committed, retained, discarded := 0, 0, 0
		for i, gen := range generations {
			state := generationState(t, pool, gen)
			switch state {
			case genCommitted:
				committed++
				if !success[i] {
					t.Fatalf("round %d: non-success generation %s committed", round, gen)
				}
				if pointerAfter != gen {
					t.Fatalf("round %d: committed generation %s is not the pointer %q", round, gen, pointerAfter)
				}
			case genRetained:
				retained++
				if !success[i] {
					t.Fatalf("round %d: non-success generation %s retained, want discarded", round, gen)
				}
			case genDiscarded:
				discarded++
				if success[i] {
					t.Fatalf("round %d: successful generation %s discarded", round, gen)
				}
			default:
				t.Fatalf("round %d: generation %s left in %q after promotion", round, gen, state)
			}
		}
		if successes > 0 && committed != 1 {
			t.Fatalf("round %d: %d successes produced %d committed generations, want exactly 1", round, successes, committed)
		}
		if successes == 0 {
			if committed != 0 {
				t.Fatalf("round %d: no successes but %d committed", round, committed)
			}
			if pointerAfter != pointerBefore {
				t.Fatalf("round %d: pointer moved %q -> %q with no successful candidate", round, pointerBefore, pointerAfter)
			}
		}
		promotedEver += committed
		retainedEver += retained
		discardedEver += discarded
	}

	// Vacuity guard: the forced rounds guarantee every branch above ran.
	if promotedEver == 0 || retainedEver == 0 || discardedEver == 0 {
		t.Fatalf("vacuous run: promoted=%d retained=%d discarded=%d — every lifecycle edge must be exercised",
			promotedEver, retainedEver, discardedEver)
	}
}
