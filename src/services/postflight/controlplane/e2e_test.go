package main

// The stage-(b) end-to-end proof: the REAL control plane (this package's
// HTTP mux, worker, and scheduler over a real PostgreSQL) exchanging syncs
// with the REAL hostd agent (its fake vm/zvol drivers standing in for the
// substrate) across real HTTP. Webhook demand in, runner exit out:
//
//	webhook(queued) -> delivery worker -> demand_recorded -> scheduler
//	  -> lease + CAS slot claim + JIT mint (fake GitHub) -> sync delivers
//	  desired lease -> hostd materializes/assigns/reports -> ready ->
//	  exited -> control plane completes, frees the slot, acks the terminal
//	  lease by omission -> hostd collects everything.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

const (
	e2eClass          = "postflight-4cpu-ubuntu-2404"
	e2eRepo           = "acme/widget"
	e2eRepoID         = int64(4242)
	e2eInstallationID = int64(123)
	e2eRunID          = int64(777)
	e2eJobID          = int64(9001)
	e2eSyncSecret     = "e2e-sync-secret"
	e2eWebhookSecret  = "e2e-webhook-secret"
	e2eJITBlob        = "e2e-encoded-jit-config"
)

// fakeGitHub scripts the GitHub API surface the loop touches: token mint,
// run + attempt-jobs reads for the worker, and the JIT config mint for the
// scheduler. Runs and jobs are mutable so a test can move a job to its
// terminal conclusion and let the refresh path observe it as API truth.
type fakeGitHub struct {
	t *testing.T

	mu       sync.Mutex
	jitMints []jitMintRequest
	jobs     map[int64]*fakeGitHubJob
}

type fakeGitHubJob struct {
	ID         int64
	RunID      int64
	Name       string
	Status     string
	Conclusion string
}

type jitMintRequest struct {
	Name          string   `json:"name"`
	RunnerGroupID int64    `json:"runner_group_id"`
	Labels        []string `json:"labels"`
}

// addQueuedJob registers a queued job (and implicitly its push run).
func (f *fakeGitHub) addQueuedJob(runID, jobID int64, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[jobID] = &fakeGitHubJob{ID: jobID, RunID: runID, Name: name, Status: "queued"}
}

// concludeJob moves a job to completed with the given conclusion.
func (f *fakeGitHub) concludeJob(jobID int64, conclusion string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[jobID].Status = "completed"
	f.jobs[jobID].Conclusion = conclusion
}

func (f *fakeGitHub) jobJSON(j *fakeGitHubJob) map[string]any {
	return map[string]any{
		"id": j.ID, "run_id": j.RunID, "run_attempt": 1,
		"name": j.Name, "status": j.Status, "conclusion": j.Conclusion,
		"labels":   []string{e2eClass},
		"head_sha": "abc123", "head_branch": "main", "workflow_name": "ci",
	}
}

func (f *fakeGitHub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("POST /app/installations/%d/access_tokens", e2eInstallationID),
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "e2e-installation-token"})
		})
	mux.HandleFunc(fmt.Sprintf("GET /repos/%s/actions/runs/{run}", e2eRepo),
		func(w http.ResponseWriter, r *http.Request) {
			runID, _ := strconv.ParseInt(r.PathValue("run"), 10, 64)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": runID, "event": "push", "path": ".github/workflows/ci.yml",
				"head_branch": "main", "head_sha": "abc123",
				"run_attempt": 1, "pull_requests": []any{},
				"head_repository": map[string]any{"full_name": e2eRepo},
			})
		})
	mux.HandleFunc(fmt.Sprintf("GET /repos/%s/actions/runs/{run}/attempts/1/jobs", e2eRepo),
		func(w http.ResponseWriter, r *http.Request) {
			runID, _ := strconv.ParseInt(r.PathValue("run"), 10, 64)
			f.mu.Lock()
			jobs := []map[string]any{}
			for _, j := range f.jobs {
				if j.RunID == runID {
					jobs = append(jobs, f.jobJSON(j))
				}
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": len(jobs),
				"jobs":        jobs,
			})
		})
	mux.HandleFunc("POST /orgs/guardian-intelligence/actions/runners/generate-jitconfig",
		func(w http.ResponseWriter, r *http.Request) {
			var mint jitMintRequest
			if err := json.NewDecoder(r.Body).Decode(&mint); err != nil {
				f.t.Errorf("jitconfig mint: %v", err)
			}
			f.mu.Lock()
			f.jitMints = append(f.jitMints, mint)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"runner":             map[string]any{"id": 7},
				"encoded_jit_config": e2eJITBlob,
			})
		})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.t.Errorf("fake github: unexpected %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	return mux
}

func (f *fakeGitHub) mints() []jitMintRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jitMintRequest(nil), f.jitMints...)
}

func testRSAKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

// controlPlane is the assembled system under test.
type controlPlane struct {
	pool   *pgxpool.Pool
	server *httptest.Server
	github *fakeGitHub
}

func startControlPlane(t *testing.T) *controlPlane {
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

	github := &fakeGitHub{t: t, jobs: map[int64]*fakeGitHubJob{}}
	github.addQueuedJob(e2eRunID, e2eJobID, "build")
	githubServer := httptest.NewServer(github.handler())
	t.Cleanup(githubServer.Close)

	cfg := config{
		appID:              1,
		installationID:     e2eInstallationID,
		webhookSecret:      e2eWebhookSecret,
		privateKeyPEM:      testRSAKeyPEM(t),
		apiBaseURL:         githubServer.URL,
		runnerClassPrefix:  "postflight-",
		workerInterval:     25 * time.Millisecond,
		workerBatchSize:    16,
		maxDeliveryTries:   8,
		hostdSyncSecret:    e2eSyncSecret,
		schedulerEnabled:   true,
		schedulerInterval:  25 * time.Millisecond,
		runnerOrg:          "guardian-intelligence",
		allocateTimeout:    10 * time.Second,
		assignmentTimeout:  90 * time.Second,
		sealTimeout:        10 * time.Second,
		verdictTimeout:     time.Hour,
		hostOfflineTimeout: 5 * time.Minute,
	}
	gh, err := newGitHubClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &pgStore{pool: pool}
	tracer := noop.NewTracerProvider().Tracer("e2e")
	ws := &webhookServer{secret: []byte(cfg.webhookSecret), inbox: st, tracer: tracer, now: time.Now}

	server := httptest.NewServer(buildMux(cfg, st, ws, tracer))
	t.Cleanup(server.Close)

	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		(&worker{st: st, gh: gh, cfg: cfg, tracer: tracer}).run(ctx)
	}()
	go func() {
		defer loops.Done()
		(&scheduler{st: st, gh: gh, cfg: cfg, tracer: tracer}).run(ctx)
	}()
	// Registered after the pool's Close so it runs first: the loops drain
	// before the database goes away under them.
	t.Cleanup(func() {
		cancel()
		loops.Wait()
	})

	return &controlPlane{pool: pool, server: server, github: github}
}

// startHostd runs the real agent loop over fake substrate against the
// control plane's HTTP server, with a pump that boots pool VMs the instant
// they launch (the only substrate transition hostd cannot observe on its
// own; runner readiness and exit stay in the test's hands).
func startHostd(t *testing.T, origin, hostID string) (*agent.Agent, *vm.Fake, *zvol.Fake) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	zvols := zvol.NewFake()
	vms := vm.NewFake()
	setAttached := func(device string, attached bool) {
		if i := strings.LastIndex(device, "/ws/"); i >= 0 {
			zvols.SetAttached(zvol.LeaseID(device[i+len("/ws/"):]), attached)
		}
	}
	vms.OnAttach = func(device string) { setAttached(device, true) }
	vms.OnDetach = func(device string) { setAttached(device, false) }

	instance, err := agent.New(agent.Config{
		HostID:              hostID,
		ControlPlaneOrigin:  origin,
		Slots:               map[vm.Class]int{e2eClass: 2},
		SyncInterval:        25 * time.Millisecond,
		CheckoutGuestOrigin: "http://198.51.100.1:8480",
	}, zvols, vms, e2eSyncSecret, []byte("0123456789abcdef0123456789abcdef"), agent.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = instance.Run(ctx) }()
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			statuses, err := vms.List(ctx)
			if err != nil {
				continue
			}
			for _, status := range statuses {
				if status.Phase == vm.PhaseBooting {
					vms.AdvanceBoot(status.ID)
				}
			}
		}
	}()
	return instance, vms, zvols
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (cp *controlPlane) queryString(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := cp.pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		return ""
	}
	return out
}

func (cp *controlPlane) postWebhook(t *testing.T, deliveryID string, payload []byte) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(e2eWebhookSecret))
	mac.Write(payload)
	req, err := http.NewRequest(http.MethodPost, cp.server.URL+"/api/v1/github/webhooks", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("webhook responded %d: %s", resp.StatusCode, body)
	}
}

func workflowJobEventPayload(action string, runID, jobID int64, status string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"action":       action,
		"installation": map[string]any{"id": e2eInstallationID},
		"repository":   map[string]any{"id": e2eRepoID, "full_name": e2eRepo},
		"workflow_job": map[string]any{
			"id": jobID, "run_id": runID, "run_attempt": 1,
			"name": "build", "status": status, "labels": []string{e2eClass},
			"head_sha": "abc123", "head_branch": "main", "workflow_name": "ci",
		},
	})
	return payload
}

func queuedJobPayload() []byte {
	return workflowJobEventPayload("queued", e2eRunID, e2eJobID, "queued")
}

func TestHostdSchedulerEndToEnd(t *testing.T) {
	cp := startControlPlane(t)
	hostd, vms, zvols := startHostd(t, cp.server.URL, "host-e2e")

	// The host must be registered with free slots before demand arrives, or
	// the allocate deadline measures sync latency instead of capacity.
	waitFor(t, "host slot registration", func() bool {
		return cp.queryString(t, `SELECT total::text FROM host_slots WHERE host_id = 'host-e2e' AND class = $1`, e2eClass) == "2"
	})

	cp.postWebhook(t, "e2e-delivery-1", queuedJobPayload())

	waitFor(t, "demand recorded and lease assigned", func() bool {
		return cp.queryString(t, `SELECT state FROM host_leases WHERE provider_job_id = $1`, e2eJobID) == "assigned"
	})
	leaseID := cp.queryString(t, `SELECT lease_id FROM host_leases WHERE provider_job_id = $1`, e2eJobID)

	// The desired lease reaches hostd over the sync exchange, carrying the
	// minted JIT config into the guest assignment.
	var vmID vm.ID
	waitFor(t, "hostd assigning the lease to a warm VM", func() bool {
		for _, snapshot := range hostd.Snapshot() {
			if snapshot.LeaseID == leaseID && snapshot.VMID != "" {
				vmID = vm.ID(snapshot.VMID)
				return true
			}
		}
		return false
	})
	assignment, ok := vms.Assignment(vmID)
	if !ok || assignment.JITConfig != e2eJITBlob {
		t.Fatalf("assignment did not carry the minted jit config: %+v (ok=%v)", assignment, ok)
	}
	mints := cp.github.mints()
	if len(mints) != 1 || mints[0].Name != leaseID || mints[0].RunnerGroupID != 1 ||
		len(mints[0].Labels) != 1 || mints[0].Labels[0] != e2eClass {
		t.Fatalf("jit mint request: %+v", mints)
	}

	// Runner registers: hostd reports ready, the control plane records it.
	vms.MarkReady(vmID)
	waitFor(t, "control plane observing ready", func() bool {
		return cp.queryString(t, `SELECT state FROM host_leases WHERE lease_id = $1`, leaseID) == "ready"
	})

	// Runner finishes: exited flows up, the control plane completes the
	// lease, frees the slot, completes the demand, and acks the terminal
	// lease by omitting it — which lets hostd collect the workspace and
	// forget the lease entirely.
	vms.MarkExited(vmID, 0)
	waitFor(t, "lease completion", func() bool {
		return cp.queryString(t, `SELECT state || ':' || exit_code::text FROM host_leases WHERE lease_id = $1`, leaseID) == "completed:0"
	})
	waitFor(t, "demand completion", func() bool {
		return cp.queryString(t, `SELECT state FROM github_provider_demands WHERE provider_job_id = $1`, e2eJobID) == "completed"
	})
	waitFor(t, "slot release", func() bool {
		return cp.queryString(t, `SELECT reserved::text FROM host_slots WHERE host_id = 'host-e2e' AND class = $1`, e2eClass) == "0"
	})
	waitFor(t, "hostd forgetting the acked lease", func() bool {
		return len(hostd.Snapshot()) == 0 && !zvols.HasWorkspace(zvol.LeaseID(leaseID))
	})
}

// TestWorkspaceGenerationLifecycleEndToEnd drives the cache loop through
// the real stack twice: the first green run cold-seeds the scope's lineage
// (seal on the host, promotion once GitHub's success is observed from the
// API), the second run clones the promoted generation, and its promotion
// retires the first generation all the way to host-confirmed destruction.
func TestWorkspaceGenerationLifecycleEndToEnd(t *testing.T) {
	cp := startControlPlane(t)
	hostd, vms, zvols := startHostd(t, cp.server.URL, "host-gen")

	waitFor(t, "host slot registration", func() bool {
		return cp.queryString(t, `SELECT total::text FROM host_slots WHERE host_id = 'host-gen' AND class = $1`, e2eClass) == "2"
	})

	// runGreenJob dispatches one queued job, walks it to a zero exit, waits
	// for the host-confirmed seal, then feeds GitHub's success verdict to
	// the refresh path and waits for the promotion CAS.
	runGreenJob := func(delivery string, runID, jobID int64) (leaseID, generation string) {
		t.Helper()
		cp.github.addQueuedJob(runID, jobID, "build")
		cp.postWebhook(t, delivery+"-queued", workflowJobEventPayload("queued", runID, jobID, "queued"))
		waitFor(t, "lease assigned", func() bool {
			return cp.queryString(t, `SELECT state FROM host_leases WHERE provider_job_id = $1`, jobID) == "assigned"
		})
		leaseID = cp.queryString(t, `SELECT lease_id FROM host_leases WHERE provider_job_id = $1`, jobID)
		var vmID vm.ID
		waitFor(t, "hostd assigning the lease to a warm VM", func() bool {
			for _, snapshot := range hostd.Snapshot() {
				if snapshot.LeaseID == leaseID && snapshot.VMID != "" {
					vmID = vm.ID(snapshot.VMID)
					return true
				}
			}
			return false
		})
		vms.MarkReady(vmID)
		waitFor(t, "control plane observing ready", func() bool {
			return cp.queryString(t, `SELECT state FROM host_leases WHERE lease_id = $1`, leaseID) == "ready"
		})
		vms.MarkExited(vmID, 0)
		waitFor(t, "seal confirmed and lease completed", func() bool {
			return cp.queryString(t, `SELECT state || ':' || reported_state FROM host_leases WHERE lease_id = $1`, leaseID) == "completed:sealed"
		})
		generation = cp.queryString(t, `SELECT seal_generation FROM host_leases WHERE lease_id = $1`, leaseID)
		if generation == "" {
			t.Fatal("green branch-trust run completed without a sealed generation")
		}
		cp.github.concludeJob(jobID, "success")
		cp.postWebhook(t, delivery+"-completed", workflowJobEventPayload("completed", runID, jobID, "completed"))
		waitFor(t, "promotion CAS", func() bool {
			return cp.queryString(t, `SELECT COALESCE(current_generation_id, '') FROM workspace_scopes`) == generation
		})
		return leaseID, generation
	}

	_, gen1 := runGreenJob("gen-e2e-1", 801, 8101)
	if got := cp.queryString(t, `SELECT home_host_id FROM workspace_scopes`); got != "host-gen" {
		t.Fatalf("scope home host = %q, want host-gen", got)
	}
	if !zvols.HasGeneration(zvol.GenerationID(gen1)) {
		t.Fatal("promoted generation is not resident on the host")
	}

	lease2, gen2 := runGreenJob("gen-e2e-2", 802, 8102)
	if got := cp.queryString(t, `SELECT workspace_generation FROM host_leases WHERE lease_id = $1`, lease2); got != gen1 {
		t.Fatalf("second run's workspace cloned %q, want the promoted generation %q", got, gen1)
	}
	if got := cp.queryString(t, `SELECT COALESCE(observed_source_generation, '') FROM host_leases WHERE lease_id = $1`, lease2); got != gen1 {
		t.Fatalf("second run's observed source = %q, want %q", got, gen1)
	}

	// The displaced generation retires: retained -> reapable -> reap verb ->
	// host destroys it -> inventory absence confirms 'reaped'.
	waitFor(t, "displaced generation reaped", func() bool {
		return cp.queryString(t, `SELECT state FROM workspace_generations WHERE generation = $1`, gen1) == "reaped"
	})
	if zvols.HasGeneration(zvol.GenerationID(gen1)) {
		t.Fatal("reaped generation is still resident on the host")
	}
	if !zvols.HasGeneration(zvol.GenerationID(gen2)) {
		t.Fatal("current generation was destroyed")
	}
}

// TestSyncRejectsBadBearer pins the endpoint's auth: a wrong credential is
// a 401 before any state is touched.
func TestSyncRejectsBadBearer(t *testing.T) {
	cp := startControlPlane(t)
	body, _ := json.Marshal(syncproto.SyncRequest{HostID: "host-x", BootID: "boot-x"})
	req, err := http.NewRequest(http.MethodPost, cp.server.URL+syncproto.SyncPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer not-the-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sync with a bad bearer responded %d, want 401", resp.StatusCode)
	}
	if got := cp.queryString(t, `SELECT host_id FROM hosts WHERE host_id = 'host-x'`); got != "" {
		t.Fatal("unauthenticated sync registered a host")
	}
}

// TestBootIDEchoGuard proves the misrouting defense through the real stack:
// a proxy rewrites the request's boot_id in flight, so the control plane's
// (otherwise well-formed, authenticated) response echoes an id the agent
// never sent — and the agent must drop it rather than apply full desired
// state that was not computed for its request.
func TestBootIDEchoGuard(t *testing.T) {
	cp := startControlPlane(t)

	tamper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err == nil && r.URL.Path == syncproto.SyncPath {
			request["boot_id"] = "not-the-boot-id"
			body, _ = json.Marshal(request)
		}
		upstream, err := http.NewRequestWithContext(r.Context(), r.Method, cp.server.URL+r.URL.Path, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		upstream.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(upstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(tamper.Close)

	hostd, _, _ := startHostd(t, tamper.URL, "host-tampered")

	// The control plane answers every exchange (the host even registers),
	// but no response survives the echo check: the agent never considers
	// itself synced, and every exchange counts as a failure.
	waitFor(t, "agent dropping tampered responses", func() bool {
		return hostd.Metrics().SyncFailures.Load() >= 3
	})
	if hostd.Synced() {
		t.Fatal("agent applied a response whose boot_id did not echo its request")
	}
}
