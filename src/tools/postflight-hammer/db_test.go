package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
)

// startMigratedDB boots a hermetic PostgreSQL and applies the control plane's
// real migration files, so every hammer query is tested against the schema
// the deployed control plane actually runs.
func startMigratedDB(t *testing.T) (context.Context, *dbClient, *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.Start(t)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	paths := strings.Fields(os.Getenv("HAMMER_MIGRATIONS"))
	if len(paths) == 0 {
		t.Fatalf("HAMMER_MIGRATIONS must list the control-plane migration files")
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"), p)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("migration %s: %v", p, err)
		}
		if _, err := conn.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("apply %s: %v", p, err)
		}
	}

	db, err := openDB(ctx, dsn)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(db.Close)
	return ctx, db, conn
}

func seedBattery(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	stmts := []string{
		`INSERT INTO hosts (host_id, boot_id, last_sync_at) VALUES ('h1', 'boot-1', now())`,
		`INSERT INTO host_slots (host_id, class, total, warm, used, reserved)
		 VALUES ('h1', 'postflight-4cpu-ubuntu-2404', 4, 4, 0, 0)`,
		`INSERT INTO github_webhook_deliveries (delivery_id, event_name, state, payload_sha256, payload_json,
		     provider_job_id, provider_run_id, received_at, verified_at)
		 VALUES ('d1', 'workflow_job', 'processed', 'sha', '{}'::jsonb, 101, 500, now() - interval '30 seconds', now() - interval '30 seconds')`,
		`INSERT INTO github_workflow_jobs (provider_job_id, provider_run_id, provider_run_attempt, status, conclusion)
		 VALUES (101, 500, 1, 'completed', 'success')`,
		`INSERT INTO github_provider_demands (provider_job_id, provider_repository_id, repository_full_name,
		     provider_run_id, provider_run_attempt, runner_class, state)
		 VALUES (101, 9, 'acme/demo', 500, 1, 'postflight-4cpu-ubuntu-2404', 'completed')`,
		`INSERT INTO host_leases (lease_id, provider_job_id, execution_id, attempt_id, runner_class, state,
		     reported_state, host_id, workspace_generation, seal_generation, exit_code, allocate_deadline_at)
		 VALUES ('L1', 101, '101', '1', 'postflight-4cpu-ubuntu-2404', 'completed',
		     'sealed', 'h1', '', 'gen-1', 0, now())`,
		`INSERT INTO workspace_generations (generation, host_id, runner_class, state, bytes)
		 VALUES ('gen-1', 'h1', 'postflight-4cpu-ubuntu-2404', 'committed', 1073741824)`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed: %v\n%s", err, s)
		}
	}
}

func TestQueriesAgainstRealSchema(t *testing.T) {
	ctx, db, conn := startMigratedDB(t)
	seedBattery(t, ctx, conn)
	since := time.Now().Add(-time.Hour)

	slots, err := db.SnapshotSlots(ctx)
	if err != nil || len(slots) != 1 {
		t.Fatalf("slots: %v %+v", err, slots)
	}
	if slots[0].Total != 4 || slots[0].Warm != 4 {
		t.Fatalf("slot = %+v", slots[0])
	}

	demands, err := db.DemandsSince(ctx, since)
	if err != nil || len(demands) != 1 {
		t.Fatalf("demands: %v %+v", err, demands)
	}
	if demands[0].ProviderJobID != 101 || demands[0].State != "completed" || demands[0].ProviderRunID != 500 {
		t.Fatalf("demand = %+v", demands[0])
	}

	leases, err := db.LeasesSince(ctx, since)
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases: %v %+v", err, leases)
	}
	l := leases[0]
	if l.LeaseID != "L1" || l.SealGeneration != "gen-1" || l.ExitCode == nil || *l.ExitCode != 0 {
		t.Fatalf("lease = %+v", l)
	}

	deliveries, err := db.DeliveriesSince(ctx, since)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries: %v %+v", err, deliveries)
	}
	if _, ok := deliveries["101"]; !ok {
		t.Fatalf("deliveries = %+v", deliveries)
	}

	generations, err := db.Generations(ctx)
	if err != nil || len(generations) != 1 {
		t.Fatalf("generations: %v %+v", err, generations)
	}
	g := generations[0]
	if g.Generation != "gen-1" || g.State != "committed" || g.Bytes != 1073741824 {
		t.Fatalf("generation = %+v", g)
	}

	snap, err := db.Snapshot(ctx)
	if err != nil || len(snap.Slots) != 1 || len(snap.Generations) != 1 {
		t.Fatalf("snapshot: %v %+v", err, snap)
	}
}

// TestWatchObservesDatabase drives one watch tick cycle against the real
// schema: transitions recorded, quiescence detected, final snapshot taken.
func TestWatchObservesDatabase(t *testing.T) {
	ctx, db, conn := startMigratedDB(t)
	seedBattery(t, ctx, conn)

	st := &stateFile{
		Repo: "acme/demo", Workflow: "ci.yml",
		StartedAt: time.Now().UTC().Add(-time.Minute),
		Runs:      map[string]*runRecord{},
	}
	w := &watcher{db: db, st: st, now: time.Now}

	quiescent, err := w.observeDB(ctx)
	if err != nil {
		t.Fatalf("observeDB: %v", err)
	}
	if !quiescent {
		t.Fatal("all seeded rows are terminal; watch should report quiescence")
	}
	if _, ok := st.DB.observedAt("demand", "101", "state", "completed"); !ok {
		t.Fatalf("demand transition not recorded: %+v", st.DB.Transitions)
	}
	if _, ok := st.DB.observedAt("lease", "L1", "state", "completed"); !ok {
		t.Fatalf("lease transition not recorded: %+v", st.DB.Transitions)
	}
	if _, ok := st.DB.observedAt("lease", "L1", "reported_state", "sealed"); !ok {
		t.Fatalf("reported_state transition not recorded: %+v", st.DB.Transitions)
	}
	if _, ok := st.DB.observedAt("generation", "gen-1", "state", "committed"); !ok {
		t.Fatalf("generation transition not recorded: %+v", st.DB.Transitions)
	}

	// A second pass with unchanged rows records nothing new.
	transitions := len(st.DB.Transitions)
	if _, err := w.observeDB(ctx); err != nil {
		t.Fatalf("observeDB again: %v", err)
	}
	if len(st.DB.Transitions) != transitions {
		t.Fatalf("replayed observation added transitions: %d -> %d", transitions, len(st.DB.Transitions))
	}

	// A non-terminal lease breaks quiescence.
	if _, err := conn.Exec(ctx, `INSERT INTO host_leases (lease_id, provider_job_id, execution_id, attempt_id,
	        runner_class, state, allocate_deadline_at)
	    VALUES ('L2', 102, '102', '1', 'postflight-4cpu-ubuntu-2404', 'allocating', now() + interval '2 seconds')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	quiescent, err = w.observeDB(ctx)
	if err != nil {
		t.Fatalf("observeDB: %v", err)
	}
	if quiescent {
		t.Fatal("allocating lease should break quiescence")
	}

	if err := w.finalize(ctx); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if st.DB.Final == nil || len(st.DB.Final.Slots) != 1 || len(st.DB.Final.Generations) != 1 {
		t.Fatalf("final snapshot = %+v", st.DB.Final)
	}
	if st.WatchDoneAt == nil {
		t.Fatal("finalize did not stamp completion")
	}
}
