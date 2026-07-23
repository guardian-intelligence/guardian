package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

const jobPlanHold = 10 * time.Second

// jobPlanBus uses one PostgreSQL listener per control-plane replica and
// fans changes out to every connected host. Waiting HTTP requests hold no
// database connection, so host count cannot starve the worker that commits
// the demand they are waiting for.
type jobPlanBus struct {
	mu      sync.Mutex
	changed chan struct{}
}

func newJobPlanBus(ctx context.Context, pool *pgxpool.Pool) *jobPlanBus {
	bus := &jobPlanBus{changed: make(chan struct{})}
	go bus.listen(ctx, pool)
	return bus
}

func (b *jobPlanBus) subscribe() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed
}

func (b *jobPlanBus) signal() {
	b.mu.Lock()
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

func (b *jobPlanBus) listen(ctx context.Context, pool *pgxpool.Pool) {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		conn, err := pool.Acquire(ctx)
		if err == nil {
			_, err = conn.Exec(ctx, `LISTEN postflight_job_plans`)
		}
		if err == nil {
			backoff = 100 * time.Millisecond
			b.signal()
			for ctx.Err() == nil {
				if _, err = conn.Conn().WaitForNotification(ctx); err != nil {
					break
				}
				b.signal()
			}
		}
		if conn != nil {
			conn.Release()
		}
		if ctx.Err() != nil {
			return
		}
		slog.Error("postflight.controlplane.job_plan_listener.failed", "err", err, "retry_after", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (s *syncServer) handleJobPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeProblems(w, []problem{problemMethodNotAllowed()})
		return
	}
	if !s.authorized(r) {
		writeProblems(w, []problem{problemSyncUnauthorized()})
		return
	}
	hostID := r.URL.Query().Get("host_id")
	cursor := r.URL.Query().Get("cursor")
	if hostID == "" || len(hostID) > 128 || len(cursor) > sha256.Size*2 {
		writeProblems(w, []problem{problemSyncPayloadInvalid("host_id or cursor is invalid")})
		return
	}

	for {
		changed := s.jobPlans.subscribe()
		snapshot, err := s.jobPlanSnapshot(r.Context(), hostID)
		if err != nil {
			s.syncError(w, hostID, "list job plans", err)
			return
		}
		if snapshot.Cursor != cursor || cursor == "" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(snapshot)
			return
		}
		timer := time.NewTimer(jobPlanHold)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(snapshot)
			return
		case <-r.Context().Done():
			timer.Stop()
			return
		}
	}
}

func (s *syncServer) jobPlanSnapshot(ctx context.Context, hostID string) (syncproto.JobPlanSnapshot, error) {
	rows, err := s.st.ListJobPlans(ctx, hostID)
	if err != nil {
		return syncproto.JobPlanSnapshot{}, err
	}
	plans := make([]syncproto.JobPlan, 0, len(rows))
	for _, row := range rows {
		org, _, _ := strings.Cut(row.Repository, "/")
		plan := syncproto.JobPlan{
			PlanID: row.PlanID, ExecutionID: strconv.FormatInt(row.ProviderJobID, 10),
			AttemptID: strconv.Itoa(row.RunAttempt), CheckRunID: row.CheckRunID,
			RunID: row.RunID, RunAttempt: row.RunAttempt, JobDisplayName: row.JobDisplayName,
			OrgID: org, InstallationID: row.InstallationID, RepositoryID: row.RepositoryID,
			RepositoryFullName: row.Repository, RunnerClass: row.RunnerClass,
			Workspace: syncproto.WorkspaceSpec{Generation: row.SourceGeneration, SizeBytes: row.WorkspaceBytes},
			Tool:      syncproto.WorkspaceSpec{Generation: row.SourceGeneration, SizeBytes: row.ToolBytes},
			Process:   syncproto.ProcessSpec{SizeBytes: row.ProcessBytes},
		}
		if row.SourceGeneration != "" && row.ProcessDigest != "" && row.ProcessVersion != "" {
			plan.Process.Generation = row.SourceGeneration
			plan.Process.ExpectedDigest = row.ProcessDigest
			plan.Process.ExpectedVersion = row.ProcessVersion
		}
		plans = append(plans, plan)
	}
	raw, err := json.Marshal(plans)
	if err != nil {
		return syncproto.JobPlanSnapshot{}, err
	}
	digest := sha256.Sum256(raw)
	return syncproto.JobPlanSnapshot{Cursor: hex.EncodeToString(digest[:]), Plans: plans}, nil
}
