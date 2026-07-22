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

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
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
	server := httptest.NewServer(buildMux(cfg, store, webhook, tracer))
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

func TestWarmPoolLocalAssignmentAndRecoverableRestoreEndToEnd(t *testing.T) {
	control := startE2EControlPlane(t)
	host, vms, volumes, _ := startE2EHost(t, control.server.URL)

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

func TestMemberCrashRequeuesSameJobToReplacement(t *testing.T) {
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
	waitFor(t, "first assignment requeued", func() bool {
		return queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE member_id = $1`, first.Incarnation) == "requeued"
	})
	waitFor(t, "replacement listener", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM runner_pool_members WHERE state = 'listening' AND member_id <> $1`, first.Incarnation) == "2"
	})
	statuses, err = vms.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var replacement vm.Status
	for _, status := range statuses {
		if status.Phase == vm.PhaseListening && status.Incarnation != first.Incarnation {
			replacement = status
			break
		}
	}
	if replacement.ID == "" {
		t.Fatal("no replacement listener")
	}
	replacementRunner := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, replacement.Incarnation)
	identity.RunnerName = replacementRunner
	if !vms.MarkAssigned(replacement.ID, vm.Assignment{
		RequestID: "request-retry", JobID: "protocol-job-retry", CheckRunID: e2eCheckRunID, RunnerName: replacementRunner,
		JobDisplayName: "build", Identity: identity,
	}) {
		t.Fatal("assign replacement listener")
	}
	var replacementAssignment string
	waitFor(t, "replacement rendezvous", func() bool {
		rendezvous, found := vms.RendezvousFor(replacement.ID)
		replacementAssignment = rendezvous.AssignmentID
		return found && replacementAssignment != ""
	})
	if got := queryString(t, control.pool, `SELECT count(*)::text FROM runner_job_assignments WHERE provider_job_id = $1`, e2eJobID); got != "2" {
		t.Fatalf("assignment attempts = %s, want 2", got)
	}
	waitFor(t, "retry remains bound", func() bool {
		state := queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE assignment_id::text = $1`, replacementAssignment)
		return state == "observed" || state == "binding"
	})
}

func TestOfflineHostRequeuesActiveAssignment(t *testing.T) {
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
	if recovered != 1 {
		t.Fatalf("recovered assignments = %d, want 1", recovered)
	}
	if got := queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE member_id = $1`, selected.Incarnation); got != "requeued" {
		t.Fatalf("assignment state = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT state FROM github_job_intents WHERE provider_job_id = $1`, e2eJobID); got != "requeued" {
		t.Fatalf("intent state = %q", got)
	}
	if got := queryString(t, control.pool, `SELECT state FROM runner_pool_members WHERE member_id = $1`, selected.Incarnation); got != "lost" {
		t.Fatalf("member state = %q", got)
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
