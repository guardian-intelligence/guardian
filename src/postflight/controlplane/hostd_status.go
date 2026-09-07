package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const hostdStatusPath = "/api/v1/hostd/status"

// The operator projection is deliberately separate from desired-state sync:
// it neither mutates inventory nor serializes JIT configs, credentials, raw
// webhook payloads, environment variables, or arbitrary failure strings.
// Historical detail lists are bounded to 50 rows; counts cover the complete
// host. Demand is class-wide because GitHub has not selected its host yet.
// One SQL statement gives every section the same PostgreSQL snapshot.
const sqlHostdStatus = `
SELECT jsonb_build_object(
    'observed_at', now(),
    'host', jsonb_build_object('host_id', h.host_id, 'boot_id', h.boot_id,
        'last_sync_at', h.last_sync_at, 'online', h.last_sync_at >= $2),
    'detail_limit', 50,
    'slots', COALESCE((SELECT jsonb_agg(jsonb_build_object(
        'class', s.class, 'total', s.total, 'booting', s.booting,
        'listening', s.listening, 'busy', s.busy) ORDER BY s.class)
        FROM host_slots s WHERE s.host_id = h.host_id), '[]'::jsonb),
    'pools', COALESCE((SELECT jsonb_agg(jsonb_build_object(
        'org', p.org_id, 'class', p.runner_class, 'desired_count', p.desired_count,
        'enabled', p.enabled) ORDER BY p.org_id, p.runner_class)
        FROM runner_pools p WHERE EXISTS (SELECT 1 FROM host_slots s
            WHERE s.host_id = h.host_id AND s.class = p.runner_class)), '[]'::jsonb),
    'member_counts', COALESCE((SELECT jsonb_object_agg(state, n) FROM (
        SELECT state, count(*) n FROM runner_pool_members WHERE host_id = h.host_id
        GROUP BY state) counts), '{}'::jsonb),
    'members', COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.updated_at DESC, m.member_id)
        FROM (SELECT member_id, vm_id, runner_name, runner_class, image, state, last_seen_at, updated_at
            FROM runner_pool_members WHERE host_id = h.host_id
            ORDER BY updated_at DESC, member_id LIMIT 50) m), '[]'::jsonb),
    'class_demand_counts', COALESCE((SELECT jsonb_object_agg(state, n) FROM (
        SELECT d.state, count(*) n FROM github_provider_demands d
        WHERE EXISTS (SELECT 1 FROM host_slots s
            WHERE s.host_id = h.host_id AND s.class = d.runner_class)
        GROUP BY d.state) counts), '{}'::jsonb),
    'class_demands', COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.updated_at DESC, d.provider_job_id)
        FROM (SELECT provider_job_id, repository_full_name AS repository,
            provider_run_id AS run_id, provider_run_attempt AS run_attempt,
            runner_class, state, source_generation, updated_at
            FROM github_provider_demands d WHERE EXISTS (SELECT 1 FROM host_slots s
                WHERE s.host_id = h.host_id AND s.class = d.runner_class)
            ORDER BY updated_at DESC, provider_job_id LIMIT 50) d), '[]'::jsonb),
    'assignment_counts', COALESCE((SELECT jsonb_object_agg(state, n) FROM (
        SELECT state, count(*) n FROM runner_job_assignments WHERE host_id = h.host_id
        GROUP BY state) counts), '{}'::jsonb),
    'assignments', COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.updated_at DESC, a.assignment_id)
        FROM (SELECT a.assignment_id, a.member_id, a.provider_job_id, a.state,
            d.repository_full_name AS repository, d.provider_run_id AS run_id,
            d.provider_run_attempt AS run_attempt, d.runner_class,
            a.source_generation, a.seal_generation, a.workspace_scope_id,
            a.restore_outcome, a.transfer_outcome, a.exit_code,
            j.status AS github_status, j.conclusion AS github_conclusion,
            j.terminal_observed_from_api_at, a.updated_at
            FROM runner_job_assignments a
            JOIN github_provider_demands d USING (provider_job_id)
            JOIN github_workflow_jobs j USING (provider_job_id)
            WHERE a.host_id = h.host_id
            ORDER BY a.updated_at DESC, a.assignment_id LIMIT 50) a), '[]'::jsonb),
    'generation_counts', COALESCE((SELECT jsonb_object_agg(state, n) FROM (
        SELECT state, count(*) n FROM workspace_generations WHERE host_id = h.host_id
        GROUP BY state) counts), '{}'::jsonb),
    'generations', COALESCE((SELECT jsonb_agg(to_jsonb(g) ORDER BY g.updated_at DESC, g.generation)
        FROM (SELECT generation, runner_class, state, scope_id, source_generation,
            bytes, sealed_at, last_used_at, updated_at
            FROM workspace_generations WHERE host_id = h.host_id
            ORDER BY updated_at DESC, generation LIMIT 50) g), '[]'::jsonb),
    'scopes', COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.updated_at DESC, s.scope_id)
        FROM (SELECT scope_id, org, repo, scope_ref, workflow_path, job_name,
            matrix_key, runner_class, workspace_epoch, current_generation_id, updated_at
            FROM workspace_scopes WHERE home_host_id = h.host_id
            ORDER BY updated_at DESC, scope_id LIMIT 50) s), '[]'::jsonb)
)
FROM hosts h WHERE h.host_id = $1`

func (s *syncServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeProblems(w, []problem{problemSyncUnauthorized()})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeProblems(w, []problem{problemMethodNotAllowed()})
		return
	}
	hostID := r.URL.Query().Get("host_id")
	if hostID == "" || len(hostID) > 128 {
		writeProblems(w, []problem{problemSyncPayloadInvalid("host_id is required and must not exceed 128 bytes")})
		return
	}
	var body []byte
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	err := s.st.pool.QueryRow(ctx, sqlHostdStatus, hostID,
		time.Now().Add(-s.hostOfflineTimeout)).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "host not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.syncError(w, hostID, "operator status", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
