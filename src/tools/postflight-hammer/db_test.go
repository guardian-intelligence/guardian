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
		`INSERT INTO host_slots (host_id, class, total, booting, listening, busy)
		 VALUES ('h1', 'postflight-4cpu-ubuntu-2404', 4, 0, 4, 0)`,
		`INSERT INTO github_webhook_deliveries (delivery_id, event_name, state, payload_sha256, payload_json,
		     provider_installation_id, provider_job_id, provider_run_id, received_at, verified_at)
		 VALUES ('d1', 'workflow_job', 'processed', 'sha', '{}'::jsonb, 42, 101, 500, now() - interval '30 seconds', now() - interval '30 seconds')`,
		`INSERT INTO github_workflow_jobs (provider_job_id, provider_installation_id, provider_run_id,
		     provider_run_attempt, status, conclusion, check_run_id)
		 VALUES (101, 42, 500, 1, 'completed', 'success', 9101)`,
		`INSERT INTO github_provider_demands (provider_job_id, provider_repository_id, repository_full_name,
		     provider_installation_id, provider_run_id, provider_run_attempt, runner_class, state)
		 VALUES (101, 9, 'acme/demo', 42, 500, 1, 'postflight-4cpu-ubuntu-2404', 'completed')`,
		`INSERT INTO runner_pools (pool_id, org_id, installation_id, runner_class, desired_count)
		 VALUES ('10000000-0000-0000-0000-000000000001', 'acme', 42,
		     'postflight-4cpu-ubuntu-2404', 4)`,
		`INSERT INTO runner_pool_members (member_id, host_id, vm_id, pool_id, runner_name,
		     runner_class, state)
		 VALUES ('member-1', 'h1', 'vm-1', '10000000-0000-0000-0000-000000000001',
		     'postflight-member-1', 'postflight-4cpu-ubuntu-2404', 'recycling')`,
		`INSERT INTO github_job_intents (provider_job_id, runner_class, repository_full_name,
		     provider_run_id, provider_run_attempt, job_display_name, check_run_id, state,
		     request_id, protocol_job_id)
		 VALUES (101, 'postflight-4cpu-ubuntu-2404', 'acme/demo', 500, 1, 'build', 9101,
		     'completed', 'request-1', 'job-1')`,
		`INSERT INTO runner_job_assignments (assignment_id, member_id, provider_job_id, host_id,
		     request_id, protocol_job_id, check_run_id, runner_name, job_display_name, run_id,
		     run_attempt, repository, workflow_job, state, seal_generation, exit_code)
		 VALUES ('20000000-0000-0000-0000-000000000001', 'member-1', 101, 'h1',
		     'request-1', 'job-1', 9101, 'postflight-member-1', 'build', '500', 1,
		     'acme/demo', 'build', 'sealed', 'gen-1', 0)`,
		`INSERT INTO workspace_generations (generation, host_id, runner_class, state, bytes)
		 VALUES ('gen-1', 'h1', 'postflight-4cpu-ubuntu-2404', 'committed', 1073741824)`,
		`INSERT INTO workspace_scopes (org, repo, scope_ref, workflow_path, job_name, runner_class,
		     current_generation_id, home_host_id)
		 VALUES ('acme', 'demo', 'refs/heads/main', 'ci.yml', 'build', 'postflight-4cpu-ubuntu-2404',
		     'gen-1', 'h1')`,
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
	if slots[0].Total != 4 || slots[0].Listening != 4 {
		t.Fatalf("slot = %+v", slots[0])
	}

	demands, err := db.DemandsSince(ctx, since)
	if err != nil || len(demands) != 1 {
		t.Fatalf("demands: %v %+v", err, demands)
	}
	if demands[0].ProviderJobID != 101 || demands[0].State != "completed" || demands[0].ProviderRunID != 500 {
		t.Fatalf("demand = %+v", demands[0])
	}

	assignments, err := db.AssignmentsSince(ctx, since)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("assignments: %v %+v", err, assignments)
	}
	assignment := assignments[0]
	if assignment.AssignmentID != "20000000-0000-0000-0000-000000000001" || assignment.SealGeneration != "gen-1" || assignment.ExitCode == nil || *assignment.ExitCode != 0 {
		t.Fatalf("assignment = %+v", assignment)
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

	scopes, err := db.Scopes(ctx)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("scopes: %v %+v", err, scopes)
	}
	s := scopes[0]
	if s.ScopeID == "" || s.Org != "acme" || s.CurrentGeneration != "gen-1" || s.HomeHostID != "h1" {
		t.Fatalf("scope = %+v", s)
	}

	snap, err := db.Snapshot(ctx)
	if err != nil || len(snap.Slots) != 1 || len(snap.Scopes) != 1 || len(snap.Generations) != 1 {
		t.Fatalf("snapshot: %v %+v", err, snap)
	}
}

func TestRunPromotedAgainstRealSchema(t *testing.T) {
	ctx, db, conn := startMigratedDB(t)
	seedBattery(t, ctx, conn)

	promoted, detail, err := runPromoted(ctx, db, time.Now().Add(-time.Hour), 500)
	if err != nil {
		t.Fatalf("runPromoted: %v", err)
	}
	if !promoted || detail != "" {
		t.Fatalf("run not promoted: promoted=%v detail=%q", promoted, detail)
	}

	if _, err := conn.Exec(ctx, `UPDATE workspace_scopes SET current_generation_id = NULL`); err != nil {
		t.Fatalf("clear scope pointer: %v", err)
	}
	promoted, detail, err = runPromoted(ctx, db, time.Now().Add(-time.Hour), 500)
	if err != nil {
		t.Fatalf("runPromoted without pointer: %v", err)
	}
	if promoted || !strings.Contains(detail, "not current") {
		t.Fatalf("unpointed generation accepted: promoted=%v detail=%q", promoted, detail)
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
	if _, ok := st.DB.observedAt("assignment", "20000000-0000-0000-0000-000000000001", "state", "sealed"); !ok {
		t.Fatalf("assignment transition not recorded: %+v", st.DB.Transitions)
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

	// A non-terminal assignment breaks quiescence.
	if _, err := conn.Exec(ctx, `INSERT INTO github_workflow_jobs (provider_job_id, provider_installation_id,
	        provider_run_id, provider_run_attempt, status, check_run_id)
	    VALUES (102, 42, 501, 1, 'in_progress', 9102)`); err != nil {
		t.Fatalf("insert workflow job: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO github_provider_demands (provider_job_id,
	        provider_repository_id, repository_full_name, provider_installation_id, provider_run_id,
	        provider_run_attempt, runner_class, state)
	    VALUES (102, 9, 'acme/demo', 42, 501, 1, 'postflight-4cpu-ubuntu-2404', 'assigned')`); err != nil {
		t.Fatalf("insert demand: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO github_job_intents (provider_job_id, runner_class,
	        repository_full_name, provider_run_id, provider_run_attempt, job_display_name,
	        check_run_id, state, request_id, protocol_job_id)
	    VALUES (102, 'postflight-4cpu-ubuntu-2404', 'acme/demo', 501, 1, 'build', 9102,
	        'running', 'request-2', 'job-2')`); err != nil {
		t.Fatalf("insert intent: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO runner_pool_members (member_id, host_id, vm_id,
	        pool_id, runner_name, runner_class, state)
	    VALUES ('member-2', 'h1', 'vm-2', '10000000-0000-0000-0000-000000000001',
	        'postflight-member-2', 'postflight-4cpu-ubuntu-2404', 'assigned')`); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO runner_job_assignments (assignment_id, member_id,
	        provider_job_id, host_id, request_id, protocol_job_id, check_run_id, runner_name,
	        job_display_name, run_id, run_attempt, repository, workflow_job, state)
	    VALUES ('20000000-0000-0000-0000-000000000002', 'member-2', 102, 'h1',
	        'request-2', 'job-2', 9102, 'postflight-member-2', 'build', '501', 1,
	        'acme/demo', 'build', 'running')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	quiescent, err = w.observeDB(ctx)
	if err != nil {
		t.Fatalf("observeDB: %v", err)
	}
	if quiescent {
		t.Fatal("running assignment should break quiescence")
	}

	if err := w.finalize(ctx); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if st.DB.Final == nil || len(st.DB.Final.Slots) != 1 || len(st.DB.Final.Scopes) != 1 || len(st.DB.Final.Generations) != 1 {
		t.Fatalf("final snapshot = %+v", st.DB.Final)
	}
	if st.WatchDoneAt == nil {
		t.Fatal("finalize did not stamp completion")
	}
}
