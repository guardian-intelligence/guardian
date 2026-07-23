package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

const (
	e2eClass          = "postflight-4-ubuntu-24.04-github-confidential"
	e2eRepo           = "acme/widget"
	e2eInstallationID = int64(123)
	e2eRepositoryID   = int64(4242)
	e2eRunID          = int64(777)
	e2eJobID          = int64(9001)
	e2eCheckRunID     = int64(8001)
	e2eSyncSecret     = "e2e-sync-secret"
	e2eJITBlob        = "e2e-encoded-jit-config"
)

type jitMintRequest struct {
	Name          string   `json:"name"`
	RunnerGroupID int64    `json:"runner_group_id"`
	Labels        []string `json:"labels"`
}

type fakeGitHub struct {
	mu    sync.Mutex
	mints []jitMintRequest
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("POST /app/installations/%d/access_tokens", e2eInstallationID), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "installation-token"})
	})
	mux.HandleFunc("POST /orgs/acme/actions/runners/generate-jitconfig", func(w http.ResponseWriter, r *http.Request) {
		var request jitMintRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode JIT request: %v", err)
		}
		f.mu.Lock()
		f.mints = append(f.mints, request)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runner": map[string]any{"id": 7}, "encoded_jit_config": e2eJITBlob,
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	return mux
}

func testRSAKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

type e2eControlPlane struct {
	pool   *pgxpool.Pool
	server *httptest.Server
	github *fakeGitHub
}

func TestProviderAcquisitionMigrationFailsHistoricalRequeuesClosed(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"001_initial.sql", "002_hostd_scheduler.sql", "003_workspace_generations.sql",
		"004_provider_installations.sql", "005_confidential_generations.sql",
		"006_durable_tool_generations.sql", "007_durable_runner_model.sql",
	} {
		if err := applyMigration(ctx, pool, name); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO hosts (host_id, boot_id, last_sync_at) VALUES ('host-migration', 'boot-1', now());
INSERT INTO runner_pools (pool_id, org_id, installation_id, runner_class, desired_count)
VALUES ('10000000-0000-0000-0000-000000000008', 'acme', $2, $1, 1);
INSERT INTO runner_pool_members (
    member_id, host_id, vm_id, pool_id, runner_name, runner_class, state
) VALUES (
    'member-migration', 'host-migration', 'vm-migration',
    '10000000-0000-0000-0000-000000000008', 'runner-migration', $1, 'lost'
);
INSERT INTO github_workflow_jobs (
    provider_job_id, provider_run_id, provider_run_attempt,
    provider_repository_id, provider_installation_id, repository_full_name,
    name, status, labels_json, runner_class, head_branch, check_run_id
) VALUES ($3, $4, 1, $5, $2, $6, 'build', 'in_progress', $7::jsonb, $1, 'main', $8);
INSERT INTO github_provider_demands (
    provider_job_id, provider_installation_id, provider_repository_id,
    repository_full_name, provider_run_id, provider_run_attempt,
    trust_class, runner_class, state
) VALUES ($3, $2, $5, $6, $4, 1, $9, $1, 'assigned');
INSERT INTO github_job_intents (
    provider_job_id, runner_class, repository_full_name, provider_run_id,
    provider_run_attempt, job_display_name, check_run_id, request_id,
    protocol_job_id, state
) VALUES ($3, $1, $6, $4, 1, 'build', $8, 'request-migration', 'job-migration', 'requeued');
INSERT INTO runner_job_assignments (
    assignment_id, member_id, provider_job_id, host_id, request_id,
    protocol_job_id, check_run_id, runner_name, job_display_name, run_id,
    run_attempt, repository, workflow_job, state
) VALUES (
    '20000000-0000-0000-0000-000000000008', 'member-migration', $3,
    'host-migration', 'request-migration', 'job-migration', $8,
    'runner-migration', 'build', $4::text, 1, $6, 'build', 'requeued'
)`, pgx.QueryExecModeSimpleProtocol, e2eClass, e2eInstallationID, e2eJobID, e2eRunID, e2eRepositoryID,
		e2eRepo, `["`+e2eClass+`"]`, e2eCheckRunID, trustClassPR); err != nil {
		t.Fatal(err)
	}

	if err := applyMigration(ctx, pool, "008_provider_acquisition_boundary.sql"); err != nil {
		t.Fatal(err)
	}
	if got := queryString(t, pool, `SELECT state FROM runner_job_assignments WHERE provider_job_id = $1`, e2eJobID); got != "failed_closed" {
		t.Fatalf("assignment state = %q", got)
	}
	if got := queryString(t, pool, `SELECT state FROM github_job_intents WHERE provider_job_id = $1`, e2eJobID); got != "failed_closed" {
		t.Fatalf("intent state = %q", got)
	}
	if got := queryString(t, pool, `SELECT state FROM github_provider_demands WHERE provider_job_id = $1`, e2eJobID); got != "sandbox_failed" {
		t.Fatalf("demand state = %q", got)
	}
	if got := queryString(t, pool, `SELECT problem_count::text || ':' || primary_problem_code FROM github_provider_demands WHERE provider_job_id = $1`, e2eJobID); got != "1:assignment.sandbox_failed" {
		t.Fatalf("demand problem = %q", got)
	}
	if got := queryString(t, pool, `SELECT count(*)::text FROM github_provider_demand_problems WHERE provider_job_id = $1 AND problem_code = 'assignment.sandbox_failed'`, e2eJobID); got != "1" {
		t.Fatalf("problem history rows = %q", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE github_job_intents SET state = 'requeued' WHERE provider_job_id = $1`, e2eJobID); err == nil {
		t.Fatal("post-migration intent accepted the retired requeued state")
	}
}

func startE2EControlPlane(t *testing.T) *e2eControlPlane {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := applyMigrations(ctx, pool); err != nil {
		cancel()
		t.Fatal(err)
	}
	seedE2EJob(t, pool)

	fake := &fakeGitHub{}
	githubServer := httptest.NewServer(fake.handler(t))
	t.Cleanup(githubServer.Close)
	cfg := config{
		appID: 1, webhookSecret: "unused", privateKeyPEM: testRSAKeyPEM(t),
		apiBaseURL: githubServer.URL, runnerClassPrefix: "postflight-",
		workerInterval: time.Hour, workerBatchSize: 16, maxDeliveryTries: 8,
		hostdSyncSecret: e2eSyncSecret, schedulerEnabled: true,
		schedulerInterval: 10 * time.Millisecond, runnerPoolSize: 2, sealTimeout: 10 * time.Second,
		verdictTimeout: time.Hour, hostOfflineTimeout: time.Minute,
	}
	client, err := newGitHubClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store := &pgStore{pool: pool}
	tracer := noop.NewTracerProvider().Tracer("e2e")
	webhook := &webhookServer{secret: []byte("unused"), inbox: store, tracer: tracer, now: time.Now}
	server := httptest.NewServer(buildMux(ctx, cfg, store, webhook, tracer))
	t.Cleanup(server.Close)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&scheduler{st: store, gh: client, cfg: cfg, tracer: tracer}).run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return &e2eControlPlane{pool: pool, server: server, github: fake}
}

func seedE2EJob(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
INSERT INTO github_workflow_jobs (
    provider_job_id, provider_run_id, provider_run_attempt,
    provider_repository_id, provider_installation_id, repository_full_name,
    name, status, labels_json, runner_class, head_branch, check_run_id
) VALUES ($1, $2, 1, $3, $4, $5, 'retired', 'completed', $6::jsonb, $7, 'main', $8)`,
		e2eJobID-1, e2eRunID-1, e2eRepositoryID, e2eInstallationID, e2eRepo,
		`["postflight-retired"]`, "postflight-retired", e2eCheckRunID-1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO github_provider_demands (
    provider_job_id, provider_installation_id, provider_repository_id,
    repository_full_name, provider_run_id, provider_run_attempt,
    trust_class, runner_class, state
) VALUES ($1, $2, $3, $4, $5, 1, $6, $7, 'completed')`,
		e2eJobID-1, e2eInstallationID, e2eRepositoryID, e2eRepo, e2eRunID-1,
		trustClassPR, "postflight-retired"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO github_workflow_jobs (
    provider_job_id, provider_run_id, provider_run_attempt,
    provider_repository_id, provider_installation_id, repository_full_name,
    name, status, labels_json, runner_class, head_branch, check_run_id
) VALUES ($1, $2, 1, $3, $4, $5, 'build', 'queued', $6::jsonb, $7, 'main', $8)`,
		e2eJobID, e2eRunID, e2eRepositoryID, e2eInstallationID, e2eRepo,
		`["`+e2eClass+`"]`, e2eClass, e2eCheckRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO github_provider_demands (
    provider_job_id, provider_installation_id, provider_repository_id,
    repository_full_name, provider_run_id, provider_run_attempt,
    trust_class, runner_class, state
) VALUES ($1, $2, $3, $4, $5, 1, $6, $7, 'demand_recorded')`,
		e2eJobID, e2eInstallationID, e2eRepositoryID, e2eRepo, e2eRunID,
		trustClassPR, e2eClass); err != nil {
		t.Fatal(err)
	}
}

func startE2EHost(t *testing.T, origin string) (*agent.Agent, *vm.Fake, *zvol.Fake, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	volumes := zvol.NewFake()
	vms := vm.NewFake()
	vms.Images[e2eClass] = "golden"
	vms.OnAttach = func(device string) {
		index := strings.LastIndex(device, "/ws/")
		if index < 0 {
			return
		}
		id := zvol.AssignmentID(device[index+len("/ws/"):])
		switch {
		case strings.Contains(device[:index], "/process-state"):
			volumes.SetProcessAttached(id, true)
		case strings.Contains(device[:index], "/tool-state"):
			volumes.SetToolAttached(id, true)
		default:
			volumes.SetAttached(id, true)
		}
	}
	vms.OnDetach = func(device string) {
		index := strings.LastIndex(device, "/ws/")
		if index < 0 {
			return
		}
		id := zvol.AssignmentID(device[index+len("/ws/"):])
		volumes.SetAttached(id, false)
		volumes.SetProcessAttached(id, false)
		volumes.SetToolAttached(id, false)
	}
	instance, err := agent.New(agent.Config{
		HostID: "host-e2e", ControlPlaneOrigin: origin,
		Slots: map[vm.Class]int{e2eClass: 2}, Images: map[vm.Class]string{e2eClass: "golden"},
		SyncInterval: 20 * time.Millisecond, CheckoutGuestOrigin: "http://198.51.100.1:8480",
	}, volumes, vms, e2eSyncSecret, make([]byte, 32), agent.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = instance.Run(ctx)
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			statuses, _ := vms.List(ctx)
			for _, status := range statuses {
				switch status.Phase {
				case vm.PhaseBooting:
					vms.AdvanceBoot(status.ID)
				case vm.PhaseAssigned:
					vms.MarkListening(status.ID)
				}
			}
		}
	}()
	return instance, vms, volumes, stop
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func queryString(t *testing.T, pool *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		return ""
	}
	return value
}

func postHostSync(t *testing.T, origin string, request syncproto.SyncRequest) syncproto.SyncResponse {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, origin+syncproto.SyncPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+e2eSyncSecret)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(response.Body)
		t.Fatalf("host sync returned %s: %s", response.Status, detail)
	}
	var desired syncproto.SyncResponse
	if err := json.NewDecoder(response.Body).Decode(&desired); err != nil {
		t.Fatal(err)
	}
	return desired
}

func TestWarmPoolLocalAssignmentAndRecoverableRestoreEndToEnd(t *testing.T) {
	control := startE2EControlPlane(t)
	const sourceGeneration = "generation-source"
	const sourceProcessDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	scopeID, err := (&pgStore{pool: control.pool}).EnsureWorkspaceScope(context.Background(), workspaceScopeKey{
		Org: "acme", Repo: "widget", ScopeRef: "main", WorkflowPath: ".github/workflows/ci.yml",
		JobName: "build", RunnerClass: e2eClass,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.pool.Exec(context.Background(), `UPDATE github_provider_demands SET workspace_scope_id = $2::uuid WHERE provider_job_id = $1`, e2eJobID, scopeID); err != nil {
		t.Fatal(err)
	}
	if _, err := control.pool.Exec(context.Background(), `
INSERT INTO workspace_generations (
    generation, host_id, runner_class, state, scope_id, process_digest, criu_version,
    sealed_at
) VALUES ($1, 'host-e2e', $2, 'committed', $3::uuid, $4, 'Version: 4.2', now())`, sourceGeneration, e2eClass, scopeID, sourceProcessDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := control.pool.Exec(context.Background(), `
UPDATE workspace_scopes SET current_generation_id = $1, home_host_id = 'host-e2e'
WHERE scope_id = $2::uuid`, sourceGeneration, scopeID); err != nil {
		t.Fatal(err)
	}
	if _, err := control.pool.Exec(context.Background(), `
UPDATE github_provider_demands SET source_generation = $1
WHERE provider_job_id = $2`, sourceGeneration, e2eJobID); err != nil {
		t.Fatal(err)
	}
	if _, err := control.pool.Exec(context.Background(), `
UPDATE github_workflow_jobs SET status = 'in_progress'
WHERE provider_job_id = $1`, e2eJobID); err != nil {
		t.Fatal(err)
	}
	if err := (&pgStore{pool: control.pool}).NotifyJobPlans(context.Background()); err != nil {
		t.Fatal(err)
	}
	host, vms, volumes, _ := startE2EHost(t, control.server.URL)
	volumes.SeedGeneration(sourceGeneration, 1<<30)
	waitFor(t, "two registered listeners", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening'`) == "2"
	})
	statuses, err := vms.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var selected vm.Status
	for _, status := range statuses {
		if status.Phase == vm.PhaseListening {
			selected = status
			break
		}
	}
	if selected.ID == "" {
		t.Fatal("no listening VM")
	}
	runnerName := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, selected.Incarnation)
	if runnerName == "" {
		t.Fatal("pool member has no runner name")
	}
	if len(control.github.mints) != 2 {
		t.Fatalf("JIT mints = %+v", control.github.mints)
	}
	if len(host.Snapshot()) != 0 {
		t.Fatal("job assignment exists before GitHub selected a listener")
	}

	identity := vm.JobIdentity{
		RunID: "777", RunAttempt: 1, RunnerName: runnerName,
		Repository: e2eRepo, WorkflowJob: "build",
	}
	if !vms.MarkAssigned(selected.ID, vm.Assignment{
		RequestID: "request-9001", JobID: "protocol-job-9001", CheckRunID: e2eCheckRunID, RunnerName: runnerName,
		JobDisplayName: "build", Identity: identity,
		Timing: []vm.TimingPoint{{
			Event: "runner_assignment_received", Source: "runner-listener", BootID: "guest-boot",
			Sequence: 1, MonotonicNS: 100, UnixNS: time.Now().UnixNano(),
		}},
	}) {
		t.Fatal("local listener did not accept assignment")
	}
	var rendezvous vm.Rendezvous
	waitFor(t, "rendezvous", func() bool {
		var found bool
		rendezvous, found = vms.RendezvousFor(selected.ID)
		return found
	})
	if rendezvous.MemberID != selected.Incarnation || rendezvous.AssignmentID == "" {
		t.Fatalf("rendezvous = %+v", rendezvous)
	}
	if !volumes.HasWorkspace(zvol.AssignmentID(rendezvous.AssignmentID)) {
		t.Fatal("assignment did not materialize durable workspace")
	}
	if !vms.MarkBoundWithRestore(selected.ID, guestproto.RestoreStatus{
		Outcome: guestproto.RestoreColdFallback, ProcessInvalidated: true,
		FailureClass: "incompatible", FailureCode: "criu-rejected",
	}) {
		t.Fatal("recoverable restore did not reach cold capsule")
	}
	waitFor(t, "exact authorization", func() bool {
		authorization, found := vms.AuthorizationFor(selected.ID)
		return found && authorization.MemberID == selected.Incarnation &&
			authorization.AssignmentID == rendezvous.AssignmentID &&
			authorization.RequestID == "request-9001"
	})
	clock := vm.ClockSample{UnixNS: time.Now().UnixNano(), Synchronized: true, Clocksource: "kvm-clock"}
	if !vms.MarkWorkerReady(selected.ID, clock) || !vms.MarkHookBlocked(selected.ID, identity) || !vms.MarkReady(selected.ID, clock) {
		t.Fatal("customer worker was not released")
	}
	waitFor(t, "running assignment", func() bool {
		return queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE assignment_id::text = $1`, rendezvous.AssignmentID) == "running"
	})
	if host.Metrics().ColdFallbacks.Load() != 1 {
		t.Fatalf("cold fallback metric = %d", host.Metrics().ColdFallbacks.Load())
	}
	if got := queryString(t, control.pool, `SELECT restore_outcome || ':' || process_invalidated::text FROM runner_job_assignments WHERE assignment_id::text = $1`, rendezvous.AssignmentID); got != "cold-fallback:true" {
		t.Fatalf("restore telemetry = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT process_valid::text || ':' || process_invalidation_class || ':' || process_invalidation_code FROM workspace_generations WHERE generation = $1`, sourceGeneration); got != "false:incompatible:criu-rejected" {
		t.Fatalf("source process invalidation = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT source_process_digest FROM runner_job_assignments WHERE assignment_id::text = $1`, rendezvous.AssignmentID); got != sourceProcessDigest {
		t.Fatalf("immutable assignment lost selected process digest = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT timing_json->0->>'event' FROM runner_job_assignments WHERE assignment_id::text = $1`, rendezvous.AssignmentID); got != "runner_assignment_received" {
		t.Fatalf("assignment timing = %q", got)
	}

	if !vms.MarkExited(selected.ID, 0) {
		t.Fatal("runner did not exit")
	}
	waitFor(t, "customer completion", func() bool {
		return queryString(t, control.pool, `SELECT state FROM github_provider_demands WHERE provider_job_id = $1`, e2eJobID) == "completed"
	})
	waitFor(t, "assignment volume cleanup", func() bool {
		return !volumes.HasWorkspace(zvol.AssignmentID(rendezvous.AssignmentID))
	})

	const retryJobID = int64(9002)
	const retryCheckRunID = int64(8002)
	if _, err := control.pool.Exec(context.Background(), `
INSERT INTO github_workflow_jobs (
    provider_job_id, provider_run_id, provider_run_attempt,
    provider_repository_id, provider_installation_id, repository_full_name,
    name, status, labels_json, runner_class, head_branch, check_run_id
) VALUES ($1, 778, 1, $2, $3, $4, 'build', 'queued', $5::jsonb, $6, 'main', $7)`,
		retryJobID, e2eRepositoryID, e2eInstallationID, e2eRepo,
		`["`+e2eClass+`"]`, e2eClass, retryCheckRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := control.pool.Exec(context.Background(), `
INSERT INTO github_provider_demands (
    provider_job_id, provider_installation_id, provider_repository_id,
    repository_full_name, provider_run_id, provider_run_attempt,
    trust_class, runner_class, workspace_scope_id, source_generation, state
) VALUES ($1, $2, $3, $4, 778, 1, $5, $6, $7::uuid, $8, 'demand_recorded')`,
		retryJobID, e2eInstallationID, e2eRepositoryID, e2eRepo,
		trustClassPR, e2eClass, scopeID, sourceGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := (&pgStore{pool: control.pool}).EnsureJobIntents(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replacement listeners", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening'`) == "2"
	})
	statuses, err = vms.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var retryMember vm.Status
	for _, status := range statuses {
		if status.Phase == vm.PhaseListening {
			retryMember = status
			break
		}
	}
	retryRunner := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, retryMember.Incarnation)
	if !vms.MarkAssigned(retryMember.ID, vm.Assignment{
		RequestID: "request-9002", JobID: "protocol-job-9002", CheckRunID: retryCheckRunID,
		RunnerName: retryRunner, JobDisplayName: "build",
		Identity: vm.JobIdentity{
			RunID: "778", RunAttempt: 1, RunnerName: retryRunner,
			Repository: e2eRepo, WorkflowJob: "build",
		},
	}) {
		t.Fatal("replacement listener did not accept assignment")
	}
	var retryRendezvous vm.Rendezvous
	waitFor(t, "workspace-warm process-cold rendezvous", func() bool {
		var found bool
		retryRendezvous, found = vms.RendezvousFor(retryMember.ID)
		return found
	})
	if retryRendezvous.CheckpointDigest != "" || retryRendezvous.CheckpointVersion != "" {
		t.Fatalf("invalidated process artifact was selected again: %+v", retryRendezvous)
	}
	_, workspaces, err := volumes.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var retriedWorkspace zvol.WorkspaceVolume
	for _, workspace := range workspaces {
		if strings.HasSuffix(workspace.Name, "/ws/"+retryRendezvous.AssignmentID) {
			retriedWorkspace = workspace
			break
		}
	}
	if retriedWorkspace.Source != zvol.GenerationID(sourceGeneration) {
		t.Fatalf("valid workspace cache was discarded with process image: %+v", retriedWorkspace)
	}
}

func TestIntegrityFailureFailsClosedAndRefillsPool(t *testing.T) {
	control := startE2EControlPlane(t)
	host, vms, _, _ := startE2EHost(t, control.server.URL)
	waitFor(t, "registered listeners", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening'`) == "2"
	})
	statuses, _ := vms.List(context.Background())
	selected := statuses[0]
	if selected.Phase != vm.PhaseListening {
		selected = statuses[1]
	}
	runnerName := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, selected.Incarnation)
	identity := vm.JobIdentity{RunID: "777", RunAttempt: 1, RunnerName: runnerName, Repository: e2eRepo, WorkflowJob: "build"}
	if !vms.MarkAssigned(selected.ID, vm.Assignment{
		RequestID: "request-9001", JobID: "protocol-job-9001", CheckRunID: e2eCheckRunID, RunnerName: runnerName,
		JobDisplayName: "build", Identity: identity,
	}) {
		t.Fatal("assign listener")
	}
	var assignmentID string
	waitFor(t, "rendezvous", func() bool {
		rendezvous, found := vms.RendezvousFor(selected.ID)
		assignmentID = rendezvous.AssignmentID
		return found
	})
	if !vms.MarkRecycleRequired(selected.ID, guestproto.RestoreStatus{
		Outcome: guestproto.RestoreUnsafe, ProcessInvalidated: true,
		FailureClass: "integrity", FailureCode: "artifact-digest",
	}, "checkpoint digest mismatch") {
		t.Fatal("mark integrity failure")
	}
	waitFor(t, "failed-closed report", func() bool {
		return queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE assignment_id::text = $1`, assignmentID) == "failed_closed"
	})
	if host.Metrics().FailedClosedAssignments.Load() != 1 {
		t.Fatalf("failed-closed metric = %d", host.Metrics().FailedClosedAssignments.Load())
	}
	waitFor(t, "replacement pool member", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening' AND member_id <> $1`, selected.Incarnation) == "2"
	})
}

func TestMemberCrashFailsAcquiredJobClosedAndRefillsPool(t *testing.T) {
	control := startE2EControlPlane(t)
	_, vms, _, _ := startE2EHost(t, control.server.URL)
	waitFor(t, "registered listeners", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening'`) == "2"
	})
	statuses, err := vms.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var first vm.Status
	for _, status := range statuses {
		if status.Phase == vm.PhaseListening {
			first = status
			break
		}
	}
	if first.ID == "" {
		t.Fatal("no first listener")
	}
	firstRunner := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, first.Incarnation)
	identity := vm.JobIdentity{RunID: "777", RunAttempt: 1, RunnerName: firstRunner, Repository: e2eRepo, WorkflowJob: "build"}
	if !vms.MarkAssigned(first.ID, vm.Assignment{
		RequestID: "request-first", JobID: "protocol-job-first", CheckRunID: e2eCheckRunID, RunnerName: firstRunner,
		JobDisplayName: "build", Identity: identity,
	}) {
		t.Fatal("assign first listener")
	}
	waitFor(t, "first rendezvous", func() bool {
		_, found := vms.RendezvousFor(first.ID)
		return found
	})
	if !vms.MarkBoundWithRestore(first.ID, guestproto.RestoreStatus{
		Outcome: guestproto.RestoreColdFallback, ProcessInvalidated: true,
		FailureClass: "incompatible", FailureCode: "missing-file",
	}) {
		t.Fatal("complete recoverable restore")
	}
	waitFor(t, "first authorization", func() bool {
		_, found := vms.AuthorizationFor(first.ID)
		return found
	})
	if err := vms.Destroy(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first assignment failed closed", func() bool {
		return queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE member_id = $1`, first.Incarnation) == "failed_closed"
	})
	waitFor(t, "replacement listener", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening' AND member_id <> $1`, first.Incarnation) == "2"
	})
	if got := queryString(t, control.pool, `SELECT count(*)::text FROM runner_job_assignments WHERE provider_job_id = $1`, e2eJobID); got != "1" {
		t.Fatalf("assignment attempts = %s, want 1", got)
	}
}

func TestOfflineHostFailsAcquiredAssignmentClosed(t *testing.T) {
	control := startE2EControlPlane(t)
	_, vms, _, stop := startE2EHost(t, control.server.URL)
	waitFor(t, "registered listeners", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening'`) == "2"
	})
	statuses, err := vms.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var selected vm.Status
	for _, status := range statuses {
		if status.Phase == vm.PhaseListening {
			selected = status
			break
		}
	}
	runnerName := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, selected.Incarnation)
	if !vms.MarkAssigned(selected.ID, vm.Assignment{
		RequestID: "request-offline", JobID: "protocol-job-offline", CheckRunID: e2eCheckRunID, RunnerName: runnerName,
		JobDisplayName: "build",
		Identity: vm.JobIdentity{
			RunID: "777", RunAttempt: 1, RunnerName: runnerName,
			Repository: e2eRepo, WorkflowJob: "build",
		},
	}) {
		t.Fatal("assign listener")
	}
	waitFor(t, "assignment persisted", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_job_assignments WHERE member_id = $1`, selected.Incarnation) == "1"
	})
	stop()
	if _, err := control.pool.Exec(context.Background(), `UPDATE hosts SET last_sync_at = now() - interval '1 hour' WHERE host_id = 'host-e2e'`); err != nil {
		t.Fatal(err)
	}
	recovered, err := (&pgStore{pool: control.pool}).RecoverOfflineHosts(context.Background(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// The live scheduler uses the same recovery transaction and may win the
	// race after last_sync_at is aged. Either caller may perform the one
	// transition; the durable state below is the invariant.
	if recovered < 0 || recovered > 1 {
		t.Fatalf("recovered assignments = %d, want at most 1", recovered)
	}
	if got := queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE member_id = $1`, selected.Incarnation); got != "failed_closed" {
		t.Fatalf("assignment state = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT state FROM github_job_intents WHERE provider_job_id = $1`, e2eJobID); got != "failed_closed" {
		t.Fatalf("intent state = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT state FROM github_provider_demands WHERE provider_job_id = $1`, e2eJobID); got != "sandbox_failed" {
		t.Fatalf("demand state = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT state FROM runner_pool_members WHERE member_id = $1`, selected.Incarnation); got != "lost" {
		t.Fatalf("member state = %q", got)
	}
	desired := postHostSync(t, control.server.URL, syncproto.SyncRequest{
		HostID: "host-e2e", BootID: "recovered-boot",
		Slots: []syncproto.SlotReport{{Class: e2eClass, Total: 2, Listening: 1}},
		Members: []syncproto.PoolMemberReport{{
			MemberID: selected.Incarnation, VMID: string(selected.ID), Class: string(selected.Class),
			Image: selected.Image, State: syncproto.MemberListening,
		}},
	})
	foundRecycle := false
	for _, member := range desired.Members {
		if member.MemberID == selected.Incarnation {
			foundRecycle = member.State == syncproto.DesiredMemberRecycle
		}
	}
	if !foundRecycle {
		t.Fatalf("lost local member was not returned for recycle: %+v", desired.Members)
	}
	desired = postHostSync(t, control.server.URL, syncproto.SyncRequest{
		HostID: "host-e2e", BootID: "recovered-boot",
		Slots: []syncproto.SlotReport{{Class: e2eClass, Total: 2}},
	})
	for _, member := range desired.Members {
		if member.MemberID == selected.Incarnation {
			t.Fatalf("absent lost member remained in desired state: %+v", desired.Members)
		}
	}
}

func TestConcurrentSameNameJobsBindByCheckRun(t *testing.T) {
	control := startE2EControlPlane(t)
	const secondJobID = int64(9002)
	const secondCheckRunID = int64(8002)
	if _, err := control.pool.Exec(context.Background(), `
INSERT INTO github_workflow_jobs (
    provider_job_id, provider_run_id, provider_run_attempt,
    provider_repository_id, provider_installation_id, repository_full_name,
    name, status, labels_json, runner_class, head_branch, check_run_id
) VALUES ($1, $2, 1, $3, $4, $5, 'build', 'queued', $6::jsonb, $7, 'main', $8)`,
		secondJobID, e2eRunID, e2eRepositoryID, e2eInstallationID, e2eRepo,
		`["`+e2eClass+`"]`, e2eClass, secondCheckRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := control.pool.Exec(context.Background(), `
INSERT INTO github_provider_demands (
    provider_job_id, provider_installation_id, provider_repository_id,
    repository_full_name, provider_run_id, provider_run_attempt,
    trust_class, runner_class, state
) VALUES ($1, $2, $3, $4, $5, 1, $6, $7, 'demand_recorded')`,
		secondJobID, e2eInstallationID, e2eRepositoryID, e2eRepo, e2eRunID,
		trustClassPR, e2eClass); err != nil {
		t.Fatal(err)
	}
	if _, err := (&pgStore{pool: control.pool}).EnsureJobIntents(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, vms, _, _ := startE2EHost(t, control.server.URL)
	waitFor(t, "two listeners", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening'`) == "2"
	})
	statuses, err := vms.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkRuns := []int64{e2eCheckRunID, secondCheckRunID}
	assigned := 0
	for _, status := range statuses {
		if status.Phase != vm.PhaseListening {
			continue
		}
		runnerName := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, status.Incarnation)
		if !vms.MarkAssigned(status.ID, vm.Assignment{
			RequestID:  fmt.Sprintf("request-concurrent-%d", assigned),
			JobID:      fmt.Sprintf("protocol-concurrent-%d", assigned),
			CheckRunID: checkRuns[assigned], RunnerName: runnerName, JobDisplayName: "build",
			Identity: vm.JobIdentity{
				RunID: "777", RunAttempt: 1, RunnerName: runnerName,
				Repository: e2eRepo, WorkflowJob: "build",
			},
		}) {
			t.Fatalf("assign concurrent listener %d", assigned)
		}
		assigned++
	}
	if assigned != 2 {
		t.Fatalf("assigned listeners = %d", assigned)
	}
	waitFor(t, "two exact bindings", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_job_assignments`) == "2"
	})
	for checkRunID, providerJobID := range map[int64]int64{
		e2eCheckRunID: e2eJobID, secondCheckRunID: secondJobID,
	} {
		if got := queryString(t, control.pool, `SELECT provider_job_id::text FROM runner_job_assignments WHERE check_run_id = $1`, checkRunID); got != fmt.Sprint(providerJobID) {
			t.Fatalf("check run %d bound provider job %q, want %d", checkRunID, got, providerJobID)
		}
	}
}
