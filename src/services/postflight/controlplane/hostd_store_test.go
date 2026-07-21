package main

// Store-level proofs for the scheduler's concurrency and hygiene invariants,
// against a real PostgreSQL. These pin the properties that make overlapping
// control-plane instances (a rolling deploy) safe: a slot claim is lease
// truth the moment it commits, the reconcile sweep never erases an in-flight
// claim, every terminal transition returns its slot and scrubs the JIT
// credential, and stuck work always terminalizes with a problem.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

const storeTestClass = "postflight-4cpu-ubuntu-2404"

func TestProviderInstallationMigrationBackfillsExistingRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{
		"001_initial.sql",
		"002_hostd_scheduler.sql",
		"003_workspace_generations.sql",
	} {
		if err := applyMigration(ctx, pool, migration); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO github_webhook_deliveries (
			delivery_id, event_name, state, payload_sha256, payload_json,
			provider_installation_id, provider_repository_id, provider_job_id,
			received_at, verified_at
		) VALUES ('delivery-1', 'workflow_job', 'processed', 'sha', '{}', 321, 77, 88, now(), now());
		INSERT INTO github_workflow_jobs (
			provider_job_id, provider_repository_id, repository_full_name
		) VALUES (88, 77, 'acme/widget');
		INSERT INTO github_provider_demands (
			provider_job_id, provider_repository_id, repository_full_name, state
		) VALUES (88, 77, 'acme/widget', 'demand_recorded');
		INSERT INTO pr_comment_state (
			provider_repository_id, pr_number, repository_full_name
		) VALUES (77, 9, 'acme/widget');
	`); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, pool, "004_provider_installations.sql"); err != nil {
		t.Fatal(err)
	}

	for table, where := range map[string]string{
		"github_workflow_jobs":    "provider_job_id = 88",
		"github_provider_demands": "provider_job_id = 88",
		"pr_comment_state":        "provider_repository_id = 77 AND pr_number = 9",
	} {
		var installationID int64
		if err := pool.QueryRow(ctx,
			`SELECT provider_installation_id FROM `+table+` WHERE `+where,
		).Scan(&installationID); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if installationID != 321 {
			t.Fatalf("%s installation = %d, want 321", table, installationID)
		}
	}
}

func startStore(t *testing.T) (*pgStore, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := applyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return &pgStore{pool: pool}, pool
}

func seedHost(t *testing.T, st *pgStore, hostID string, total int) {
	t.Helper()
	if err := st.UpsertHostSync(context.Background(), hostID, "boot-1",
		[]syncproto.SlotReport{{Class: storeTestClass, Total: total, Warm: total}}); err != nil {
		t.Fatal(err)
	}
}

func seedDemand(t *testing.T, pool *pgxpool.Pool, jobID int64, class string) schedulableDemand {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO github_workflow_jobs (provider_job_id, provider_installation_id, runner_class) VALUES ($1, 1, $2)`,
		jobID, class); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO github_provider_demands (provider_job_id, provider_installation_id, repository_full_name, provider_run_attempt, runner_class, state)
		 VALUES ($1, 1, 'acme/widget', 1, $2, 'demand_recorded')`,
		jobID, class); err != nil {
		t.Fatal(err)
	}
	return schedulableDemand{
		ProviderJobID: jobID, ProviderInstallationID: 1, ProviderRepositoryID: 1, RepositoryFullName: "acme/widget",
		ProviderRunAttempt: 1, RunnerClass: class, DiskBytes: 1 << 30,
	}
}

func mustCreateLease(t *testing.T, st *pgStore, d schedulableDemand, allocateDeadline time.Time) string {
	t.Helper()
	leaseID, created, err := st.CreateLeaseForDemand(context.Background(), d,
		fmt.Sprintf("%d", d.ProviderJobID), "1", "org", 1, allocateDeadline)
	if err != nil || !created {
		t.Fatalf("create lease: created=%v err=%v", created, err)
	}
	return leaseID
}

func reservedCount(t *testing.T, pool *pgxpool.Pool, hostID string) int {
	t.Helper()
	var reserved int
	if err := pool.QueryRow(context.Background(),
		`SELECT reserved FROM host_slots WHERE host_id = $1 AND class = $2`,
		hostID, storeTestClass).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	return reserved
}

func demandState(t *testing.T, pool *pgxpool.Pool, jobID int64) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(context.Background(),
		`SELECT state FROM github_provider_demands WHERE provider_job_id = $1`, jobID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func leaseColumn(t *testing.T, pool *pgxpool.Pool, leaseID, column string) string {
	t.Helper()
	var out string
	if err := pool.QueryRow(context.Background(),
		`SELECT `+column+` FROM host_leases WHERE lease_id = $1`, leaseID).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestReconcilePreservesInFlightClaim is the deploy-overlap proof: a second
// scheduler instance's reconcile sweep, running while a claimed lease is
// still allocating (mid-JIT-mint), must not reset the reservation — and the
// host's last slot must stay unclaimable.
func TestReconcilePreservesInFlightClaim(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 1)

	lease1 := mustCreateLease(t, st, seedDemand(t, pool, 101, storeTestClass), time.Now().Add(time.Minute))
	hostID, claimed, err := st.ClaimHostSlot(ctx, lease1, storeTestClass)
	if err != nil || !claimed || hostID != "host-a" {
		t.Fatalf("claim: host=%q claimed=%v err=%v", hostID, claimed, err)
	}

	// The overlapping instance's tick-start sweep.
	fixed, err := st.ReconcileSlotReservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 0 {
		t.Fatalf("reconcile erased an in-flight claim (%d slots corrected)", fixed)
	}

	// Its placement pass must find no capacity for a second lease.
	lease2 := mustCreateLease(t, st, seedDemand(t, pool, 102, storeTestClass), time.Now().Add(time.Minute))
	if _, claimed, err = st.ClaimHostSlot(ctx, lease2, storeTestClass); err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("second lease claimed a slot already held by an in-flight claim: double assignment")
	}
	if got := reservedCount(t, pool, "host-a"); got != 1 {
		t.Fatalf("reserved = %d, want 1", got)
	}
}

func TestObservedAssignmentAdoptsCapacityDisplacedJob(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 1)

	listenerJob := int64(111)
	actualJob := int64(112)
	listener := mustCreateLease(t, st,
		seedDemand(t, pool, listenerJob, storeTestClass),
		time.Now().Add(time.Minute))
	if _, claimed, err := st.ClaimHostSlot(ctx, listener, storeTestClass); err != nil || !claimed {
		t.Fatalf("listener claim: claimed=%v err=%v", claimed, err)
	}
	if assigned, err := st.AssignLease(ctx, listener, "host-a", "jit-listener", time.Now().Add(time.Minute)); err != nil || !assigned {
		t.Fatalf("listener assign: assigned=%v err=%v", assigned, err)
	}
	if ready, err := st.MarkLeaseReady(ctx, "host-a", listener); err != nil || !ready {
		t.Fatalf("listener ready: ready=%v err=%v", ready, err)
	}

	execution := mustCreateLease(t, st,
		seedDemand(t, pool, actualJob, storeTestClass),
		time.Now().Add(time.Minute))
	if _, claimed, err := st.ClaimHostSlot(ctx, execution, storeTestClass); err != nil {
		t.Fatal(err)
	} else if claimed {
		t.Fatal("actual job unexpectedly claimed capacity before displacement")
	}

	if err := st.UpsertJobAssignment(ctx, jobAssignment{
		ProviderJobID: actualJob,
		RunnerName:    listener,
		RunnerID:      7,
		DeliveryID:    "assignment-112",
	}); err != nil {
		t.Fatal(err)
	}
	if got := leaseColumn(t, pool, execution, "state"); got != leaseReady {
		t.Fatalf("routed execution state = %q, want ready", got)
	}
	if got := reservedCount(t, pool, "host-a"); got != 1 {
		t.Fatalf("routed execution changed listener reservation to %d, want 1", got)
	}
	desired, err := st.ListDesiredLeases(ctx, "host-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(desired) != 1 || desired[0].LeaseID != listener ||
		desired[0].ExecutionLeaseID != execution ||
		desired[0].ProviderJobID != actualJob ||
		!desired[0].RendezvousAuthorized {
		t.Fatalf("routed desired lease = %+v", desired)
	}

	if _, _, completed, err := st.CompleteRoutedLease(
		ctx, "host-a", listener, execution, 1, testCheckpoint(), time.Now().Add(time.Minute)); err != nil || !completed {
		t.Fatalf("routed completion: completed=%v err=%v", completed, err)
	}
	if got := reservedCount(t, pool, "host-a"); got != 0 {
		t.Fatalf("completed routed listener left %d reservations", got)
	}
	if got := demandState(t, pool, actualJob); got != demandCompleted {
		t.Fatalf("actual demand state = %q, want completed", got)
	}
	if got := demandState(t, pool, listenerJob); got != demandRecorded {
		t.Fatalf("displaced demand state = %q, want recorded for retry", got)
	}
	if got := leaseColumn(t, pool, listener, "state"); got != leaseExpired {
		t.Fatalf("consumed listener execution state = %q, want expired", got)
	}
}

// TestClaimBindsLeaseOnce: two instances racing to place the same lease can
// claim at most one slot between them.
func TestClaimBindsLeaseOnce(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 2)

	lease := mustCreateLease(t, st, seedDemand(t, pool, 201, storeTestClass), time.Now().Add(time.Minute))
	if _, claimed, err := st.ClaimHostSlot(ctx, lease, storeTestClass); err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	if _, claimed, err := st.ClaimHostSlot(ctx, lease, storeTestClass); err != nil {
		t.Fatal(err)
	} else if claimed {
		t.Fatal("the same lease claimed a second slot")
	}
	if got := reservedCount(t, pool, "host-a"); got != 1 {
		t.Fatalf("reserved = %d, want 1", got)
	}
}

// TestExpiredClaimReleasesSlot: a claim orphaned in 'allocating' (crash
// mid-mint) heals through the allocate-deadline expiry, which returns the
// slot in the same transaction.
func TestExpiredClaimReleasesSlot(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 1)

	lease := mustCreateLease(t, st, seedDemand(t, pool, 301, storeTestClass), time.Now().Add(-time.Second))
	if _, claimed, err := st.ClaimHostSlot(ctx, lease, storeTestClass); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}

	overdue, err := st.ListOverdueLeases(ctx, 16, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(overdue) != 1 || overdue[0].LeaseID != lease || overdue[0].State != leaseAllocating {
		t.Fatalf("overdue = %+v, want the claimed allocating lease", overdue)
	}
	expired, err := st.ExpireLease(ctx, overdue[0], "allocate deadline exceeded",
		[]problem{problemCapacityTimeout(storeTestClass)})
	if err != nil || !expired {
		t.Fatalf("expire: expired=%v err=%v", expired, err)
	}
	if got := reservedCount(t, pool, "host-a"); got != 0 {
		t.Fatalf("reserved = %d after expiring the claimed lease, want 0", got)
	}
	if got := demandState(t, pool, 301); got != demandCapacityFailed {
		t.Fatalf("demand state = %q, want %q", got, demandCapacityFailed)
	}
}

// TestReadyLeaseOnDeadHostFailsOver: a ready lease has no deadline of its
// own, so a host that permanently stops syncing must be observed as absence
// — the sweep terminalizes the lease instead of wedging the job forever.
func TestReadyLeaseOnDeadHostFailsOver(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 1)

	lease := mustCreateLease(t, st, seedDemand(t, pool, 401, storeTestClass), time.Now().Add(time.Minute))
	if _, claimed, err := st.ClaimHostSlot(ctx, lease, storeTestClass); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if assigned, err := st.AssignLease(ctx, lease, "host-a", "jit-blob", time.Now().Add(time.Minute)); err != nil || !assigned {
		t.Fatalf("assign: assigned=%v err=%v", assigned, err)
	}
	if ready, err := st.MarkLeaseReady(ctx, "host-a", lease); err != nil || !ready {
		t.Fatalf("ready: ready=%v err=%v", ready, err)
	}

	cutoff := time.Now().Add(-5 * time.Minute)
	if overdue, err := st.ListOverdueLeases(ctx, 16, cutoff); err != nil {
		t.Fatal(err)
	} else if len(overdue) != 0 {
		t.Fatalf("live host's ready lease listed as overdue: %+v", overdue)
	}

	if _, err := pool.Exec(ctx, `UPDATE hosts SET last_sync_at = now() - interval '10 minutes' WHERE host_id = 'host-a'`); err != nil {
		t.Fatal(err)
	}
	overdue, err := st.ListOverdueLeases(ctx, 16, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(overdue) != 1 || overdue[0].LeaseID != lease || overdue[0].State != leaseReady {
		t.Fatalf("overdue = %+v, want the ready lease on the dead host", overdue)
	}
	expired, err := st.ExpireLease(ctx, overdue[0], "host stopped syncing", []problem{problemHostLost("host-a")})
	if err != nil || !expired {
		t.Fatalf("expire: expired=%v err=%v", expired, err)
	}
	if got := demandState(t, pool, 401); got != demandSandboxFailed {
		t.Fatalf("demand state = %q, want %q", got, demandSandboxFailed)
	}
	if got := reservedCount(t, pool, "host-a"); got != 0 {
		t.Fatalf("reserved = %d, want 0", got)
	}
}

// TestTerminalTransitionsScrubJITConfig: the encoded runner registration
// credential must not survive the lease it was minted for, on any exit.
func TestTerminalTransitionsScrubJITConfig(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedHost(t, st, "host-a", 4)

	place := func(jobID int64, assignmentDeadline time.Time) string {
		t.Helper()
		lease := mustCreateLease(t, st, seedDemand(t, pool, jobID, storeTestClass), time.Now().Add(time.Minute))
		if _, claimed, err := st.ClaimHostSlot(ctx, lease, storeTestClass); err != nil || !claimed {
			t.Fatalf("claim: claimed=%v err=%v", claimed, err)
		}
		if assigned, err := st.AssignLease(ctx, lease, "host-a", "jit-blob", assignmentDeadline); err != nil || !assigned {
			t.Fatalf("assign: assigned=%v err=%v", assigned, err)
		}
		return lease
	}

	completed := place(501, time.Now().Add(time.Minute))
	if _, _, ok, err := st.CompleteLease(ctx, "host-a", completed, 0, testCheckpoint(), time.Now().Add(time.Minute)); err != nil || !ok {
		t.Fatalf("complete: ok=%v err=%v", ok, err)
	}
	failed := place(502, time.Now().Add(time.Minute))
	if _, ok, err := st.FailLeaseFromHost(ctx, "host-a", failed, "failed", "boom",
		[]problem{problemSandboxFailed("boom")}); err != nil || !ok {
		t.Fatalf("fail from host: ok=%v err=%v", ok, err)
	}
	expired := place(503, time.Now().Add(-time.Second))
	overdue, err := st.ListOverdueLeases(ctx, 16, time.Now().Add(-5*time.Minute))
	if err != nil || len(overdue) != 1 {
		t.Fatalf("overdue = %+v err=%v, want exactly the past-deadline assigned lease", overdue, err)
	}
	if ok, err := st.ExpireLease(ctx, overdue[0], "assignment deadline exceeded",
		[]problem{problemAssignmentTimeout()}); err != nil || !ok {
		t.Fatalf("expire: ok=%v err=%v", ok, err)
	}

	for name, lease := range map[string]string{"completed": completed, "failed": failed, "expired": expired} {
		if got := leaseColumn(t, pool, lease, "jit_config"); got != "" {
			t.Errorf("%s lease retains jit_config %q, want scrubbed", name, got)
		}
	}

	// A mint failure never had a JIT config, but its claim must be returned
	// by the terminalizing transaction itself.
	before := reservedCount(t, pool, "host-a")
	mintFailed := mustCreateLease(t, st, seedDemand(t, pool, 504, storeTestClass), time.Now().Add(time.Minute))
	if _, claimed, err := st.ClaimHostSlot(ctx, mintFailed, storeTestClass); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if ok, err := st.FailAllocatingLease(ctx, mintFailed, "jit mint: denied",
		[]problem{problemJITMintFailed(fmt.Errorf("denied"))}); err != nil || !ok {
		t.Fatalf("fail allocating: ok=%v err=%v", ok, err)
	}
	if got := reservedCount(t, pool, "host-a"); got != before {
		t.Fatalf("reserved = %d after mint failure, want %d (slot returned in the failing transaction)", got, before)
	}
}

// TestUnknownRunnerClassDemandRejected: a demand for a class with no
// runner_classes row terminalizes with a problem instead of sitting
// invisible behind the schedulable join forever.
func TestUnknownRunnerClassDemandRejected(t *testing.T) {
	ctx := context.Background()
	st, pool := startStore(t)
	seedDemand(t, pool, 601, "postflight-8cpu-ubuntu-2404")
	known := seedDemand(t, pool, 602, storeTestClass)

	sched := &scheduler{st: st, cfg: config{workerBatchSize: 16}, tracer: noop.NewTracerProvider().Tracer("test")}
	sched.rejectUnknownClasses(ctx)

	if got := demandState(t, pool, 601); got != demandCapacityFailed {
		t.Fatalf("unknown-class demand state = %q, want %q", got, demandCapacityFailed)
	}
	var code string
	if err := pool.QueryRow(ctx,
		`SELECT primary_problem_code FROM github_provider_demands WHERE provider_job_id = 601`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "demand.runner_class_unknown" {
		t.Fatalf("primary problem code = %q, want demand.runner_class_unknown", code)
	}
	if got := demandState(t, pool, known.ProviderJobID); got != demandRecorded {
		t.Fatalf("known-class demand state = %q, want untouched %q", got, demandRecorded)
	}
}
