package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

const (
	genCandidate = "candidate"
	genCommitted = "committed"
	genRetained  = "retained"
	genDiscarded = "discarded"
	genReapable  = "reapable"
	genReaped    = "reaped"

	demandRecorded          = "demand_recorded"
	demandCapacityRequested = "capacity_requested"
	demandAssigned          = "assigned"
	demandCompleted         = "completed"
	demandCapacityFailed    = "capacity_failed"
	demandJITFailed         = "jit_failed"
	demandSandboxFailed     = "sandbox_failed"
)

const (
	sqlUpsertHost = `
INSERT INTO hosts (host_id, boot_id, last_sync_at)
VALUES ($1, $2, now())
ON CONFLICT (host_id) DO UPDATE SET
    boot_id = EXCLUDED.boot_id, last_sync_at = now(), updated_at = now()`

	sqlUpsertHostSlot = `
INSERT INTO host_slots (host_id, class, total, booting, listening, busy)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (host_id, class) DO UPDATE SET
    total = EXCLUDED.total, booting = EXCLUDED.booting,
    listening = EXCLUDED.listening, busy = EXCLUDED.busy, updated_at = now()`
)

func (s *pgStore) UpsertHostSync(ctx context.Context, hostID, bootID string, slots []syncproto.SlotReport) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, sqlUpsertHost, hostID, bootID); err != nil {
		return err
	}
	classes := make([]string, 0, len(slots))
	for _, slot := range slots {
		if slot.Class == "" || slot.Total < 0 || slot.Booting < 0 || slot.Listening < 0 || slot.Busy < 0 {
			continue
		}
		classes = append(classes, slot.Class)
		if _, err := tx.Exec(ctx, sqlUpsertHostSlot, hostID, slot.Class, slot.Total, slot.Booting, slot.Listening, slot.Busy); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM host_slots WHERE host_id = $1 AND class <> ALL($2::text[])`, hostID, classes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const sqlObserveGeneration = `
INSERT INTO workspace_generations (generation, host_id, bytes, state)
VALUES ($1, $2, $3, 'candidate')
ON CONFLICT (generation) DO UPDATE SET
    host_id = EXCLUDED.host_id, bytes = EXCLUDED.bytes, updated_at = now()`

func (s *pgStore) ObserveHostGenerations(ctx context.Context, hostID string, generations []syncproto.GenerationReport) error {
	names := make([]string, 0, len(generations))
	for _, generation := range generations {
		if generation.Generation == "" {
			continue
		}
		names = append(names, generation.Generation)
		if _, err := s.pool.Exec(ctx, sqlObserveGeneration, generation.Generation, hostID, generation.Bytes); err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `
UPDATE workspace_generations SET state = 'reaped', updated_at = now()
WHERE host_id = $1 AND state = 'reapable' AND generation <> ALL($2::text[])`, hostID, names)
	return err
}

// EnsureRunnerPools turns observed GitHub installations into continuously
// registered capacity. The newest demand carries the current installation ID
// after an app reinstall; an existing pool stays warm between jobs.
func (s *pgStore) EnsureRunnerPools(ctx context.Context, desiredCount int) (int64, error) {
	if desiredCount <= 0 {
		return 0, fmt.Errorf("runner pool size must be positive")
	}
	tag, err := s.pool.Exec(ctx, `
INSERT INTO runner_pools (org_id, installation_id, runner_class, desired_count)
SELECT DISTINCT ON (split_part(repository_full_name, '/', 1), runner_class)
       split_part(repository_full_name, '/', 1), provider_installation_id,
       runner_class, $1
FROM github_provider_demands
WHERE provider_installation_id > 0
  AND split_part(repository_full_name, '/', 1) <> ''
  AND split_part(repository_full_name, '/', 2) <> ''
ORDER BY split_part(repository_full_name, '/', 1), runner_class, updated_at DESC
ON CONFLICT (org_id, runner_class) DO UPDATE SET
    installation_id = EXCLUDED.installation_id,
    desired_count = EXCLUDED.desired_count,
    enabled = true,
    updated_at = now()
WHERE runner_pools.installation_id IS DISTINCT FROM EXCLUDED.installation_id
   OR runner_pools.desired_count IS DISTINCT FROM EXCLUDED.desired_count
   OR NOT runner_pools.enabled`, desiredCount)
	return tag.RowsAffected(), err
}

// EnsureJobIntents converts provider truth into the durable queue consumed by
// local runner assignments. It is idempotent and never rewinds an assignment.
func (s *pgStore) EnsureJobIntents(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
INSERT INTO github_job_intents (
    provider_job_id, runner_class, repository_full_name, provider_run_id,
    provider_run_attempt, job_display_name, check_run_id, state
)
SELECT d.provider_job_id, d.runner_class, d.repository_full_name,
       d.provider_run_id, d.provider_run_attempt, j.name, j.check_run_id, 'queued'
FROM github_provider_demands d
JOIN github_workflow_jobs j ON j.provider_job_id = d.provider_job_id
JOIN runner_classes c ON c.class = d.runner_class
WHERE d.state IN ('demand_recorded', 'capacity_requested')
  AND j.status IN ('queued', 'in_progress')
  AND j.check_run_id > 0
ON CONFLICT (provider_job_id) DO UPDATE SET
    runner_class = EXCLUDED.runner_class,
    repository_full_name = EXCLUDED.repository_full_name,
    provider_run_id = EXCLUDED.provider_run_id,
    provider_run_attempt = EXCLUDED.provider_run_attempt,
    job_display_name = EXCLUDED.job_display_name,
    check_run_id = EXCLUDED.check_run_id,
    updated_at = now()
WHERE github_job_intents.state IN ('queued', 'requeued')`)
	return tag.RowsAffected(), err
}

type poolMemberForJIT struct {
	MemberID       string
	RunnerName     string
	RunnerClass    string
	OrgID          string
	InstallationID int64
}

func (s *pgStore) ListPoolMembersNeedingJIT(ctx context.Context, limit int) ([]poolMemberForJIT, error) {
	rows, err := s.pool.Query(ctx, `
SELECT m.member_id, m.runner_name, m.runner_class, p.org_id, p.installation_id
FROM runner_pool_members m
JOIN runner_pools p ON p.pool_id = m.pool_id
WHERE m.state = 'warm' AND m.jit_config = '' AND p.enabled
ORDER BY m.created_at
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []poolMemberForJIT
	for rows.Next() {
		var member poolMemberForJIT
		if err := rows.Scan(&member.MemberID, &member.RunnerName, &member.RunnerClass, &member.OrgID, &member.InstallationID); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *pgStore) RecordPoolMemberJIT(ctx context.Context, memberID, jit string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE runner_pool_members
SET jit_config = $2, state = 'preparing', updated_at = now()
WHERE member_id = $1 AND state = 'warm' AND jit_config = ''`, memberID, jit)
	return tag.RowsAffected() > 0, err
}

func (s *pgStore) FailPoolMemberJIT(ctx context.Context, memberID, reason string) error {
	_, err := s.pool.Exec(ctx, `
UPDATE runner_pool_members
SET state = 'recycling', reported_reason = $2, jit_config = '', updated_at = now()
WHERE member_id = $1`, memberID, reason)
	return err
}

func (s *pgStore) ApplyHostMembers(ctx context.Context, hostID string, reports []syncproto.PoolMemberReport) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	seen := make([]string, 0, len(reports))
	for _, report := range reports {
		if report.MemberID == "" || report.VMID == "" || report.Class == "" {
			continue
		}
		seen = append(seen, report.MemberID)
		if _, err := tx.Exec(ctx, `
INSERT INTO runner_pool_members (
    member_id, host_id, vm_id, runner_class, image, state, reported_reason
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (member_id) DO UPDATE SET
    image = EXCLUDED.image,
    state = CASE
        WHEN runner_pool_members.state IN ('recycling', 'lost') THEN runner_pool_members.state
        ELSE EXCLUDED.state
    END,
    reported_reason = CASE
        WHEN runner_pool_members.state IN ('recycling', 'lost') THEN runner_pool_members.reported_reason
        ELSE EXCLUDED.reported_reason
    END,
    last_seen_at = now(), updated_at = now()
WHERE runner_pool_members.host_id = EXCLUDED.host_id
  AND runner_pool_members.vm_id = EXCLUDED.vm_id
  AND runner_pool_members.runner_class = EXCLUDED.runner_class`,
			report.MemberID, hostID, report.VMID, report.Class, report.Image, string(report.State), report.Reason); err != nil {
			return err
		}
		if err := allocateMemberPool(ctx, tx, report.MemberID, report.Class); err != nil {
			return err
		}
		if report.Assignment != nil {
			if err := bindObservedAssignment(ctx, tx, hostID, report); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE runner_pool_members
SET state = 'lost', jit_config = '', updated_at = now()
WHERE host_id = $1 AND state NOT IN ('lost', 'recycling')
  AND member_id <> ALL($2::text[])`, hostID, seen); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func allocateMemberPool(ctx context.Context, tx pgx.Tx, memberID, class string) error {
	var poolID string
	err := tx.QueryRow(ctx, `
SELECT p.pool_id::text
FROM runner_pools p
WHERE p.runner_class = $1 AND p.enabled
  AND (SELECT count(*) FROM runner_pool_members m
       WHERE m.pool_id = p.pool_id AND m.state NOT IN ('lost', 'recycling')) < p.desired_count
ORDER BY p.updated_at, p.pool_id
LIMIT 1`, class).Scan(&poolID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
UPDATE runner_pool_members
SET pool_id = $2::uuid,
    runner_name = 'postflight-' || left(replace(member_id, '-', ''), 40),
    updated_at = now()
WHERE member_id = $1 AND pool_id IS NULL`, memberID, poolID)
	return err
}

func bindObservedAssignment(ctx context.Context, tx pgx.Tx, hostID string, member syncproto.PoolMemberReport) error {
	observed := member.Assignment
	if observed == nil || observed.RequestID == "" || observed.JobID == "" || observed.CheckRunID <= 0 ||
		observed.RunnerName == "" || observed.Identity.RunID == "" || observed.Identity.RunAttempt <= 0 {
		return nil
	}
	var runnerName, class, org string
	var poolID string
	err := tx.QueryRow(ctx, `
SELECT m.runner_name, m.runner_class, p.pool_id::text, p.org_id
FROM runner_pool_members m
JOIN runner_pools p ON p.pool_id = m.pool_id
WHERE m.member_id = $1 AND m.host_id = $2
FOR UPDATE OF m`, member.MemberID, hostID).Scan(&runnerName, &class, &poolID, &org)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if runnerName != observed.RunnerName || observed.Identity.RunnerName != runnerName ||
		!strings.HasPrefix(observed.Identity.Repository, org+"/") {
		_, err := tx.Exec(ctx, `UPDATE runner_pool_members SET state = 'recycling', reported_reason = 'local assignment identity mismatch', updated_at = now() WHERE member_id = $1`, member.MemberID)
		return err
	}
	if exists, err := assignmentExists(ctx, tx, member.MemberID); err != nil || exists {
		return err
	}
	runID, err := strconv.ParseInt(observed.Identity.RunID, 10, 64)
	if err != nil {
		_, updateErr := tx.Exec(ctx, `UPDATE runner_pool_members SET state = 'recycling', reported_reason = 'invalid local run id', updated_at = now() WHERE member_id = $1`, member.MemberID)
		return updateErr
	}
	rows, err := tx.Query(ctx, `
SELECT i.provider_job_id
FROM github_job_intents i
WHERE i.runner_class = $1 AND i.repository_full_name = $2
  AND i.provider_run_id = $3 AND i.provider_run_attempt = $4
  AND i.check_run_id = $5
  AND i.state IN ('queued', 'observed', 'requeued')
  AND (i.request_id = '' OR i.request_id = $6)
ORDER BY (i.request_id = $6) DESC, i.provider_job_id
LIMIT 2
FOR UPDATE`, class, observed.Identity.Repository, runID, observed.Identity.RunAttempt,
		observed.CheckRunID, observed.RequestID)
	if err != nil {
		return err
	}
	var matches []int64
	for rows.Next() {
		var jobID int64
		if err := rows.Scan(&jobID); err != nil {
			rows.Close()
			return err
		}
		matches = append(matches, jobID)
	}
	rows.Close()
	if len(matches) == 0 {
		return nil
	}
	if len(matches) != 1 {
		_, err := tx.Exec(ctx, `UPDATE runner_pool_members SET state = 'recycling', reported_reason = $2, updated_at = now() WHERE member_id = $1`, member.MemberID, fmt.Sprintf("check run matched %d job intents", len(matches)))
		return err
	}
	providerJobID := matches[0]
	if _, err := tx.Exec(ctx, `
UPDATE github_job_intents
SET request_id = $2, protocol_job_id = $3, state = 'observed', updated_at = now()
WHERE provider_job_id = $1`, providerJobID, observed.RequestID, observed.JobID); err != nil {
		return err
	}
	timing, err := json.Marshal(observed.Timing)
	if err != nil {
		return err
	}
	var assignmentID string
	if err := tx.QueryRow(ctx, `
INSERT INTO runner_job_assignments (
    member_id, provider_job_id, host_id, request_id, protocol_job_id, check_run_id,
    runner_name, job_display_name, run_id, run_attempt, repository,
    workflow_job, state, workspace_scope_id, source_generation,
    source_process_digest, source_process_version, timing_json
)
SELECT $1, i.provider_job_id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
       'observed', d.workspace_scope_id, COALESCE(s.current_generation_id, ''),
       COALESCE(CASE WHEN g.process_valid THEN g.process_digest END, ''),
       COALESCE(CASE WHEN g.process_valid THEN g.criu_version END, ''), $12::jsonb
FROM github_job_intents i
JOIN github_provider_demands d ON d.provider_job_id = i.provider_job_id
LEFT JOIN workspace_scopes s ON s.scope_id = d.workspace_scope_id
LEFT JOIN workspace_generations g ON g.generation = s.current_generation_id
WHERE i.provider_job_id = $13
RETURNING assignment_id::text`, member.MemberID, hostID, observed.RequestID, observed.JobID, observed.CheckRunID,
		runnerName, observed.JobDisplayName, observed.Identity.RunID, observed.Identity.RunAttempt,
		observed.Identity.Repository, observed.Identity.WorkflowJob, string(timing), providerJobID).Scan(&assignmentID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runner_pool_members SET state = 'assigned', updated_at = now() WHERE member_id = $1`, member.MemberID)
	return err
}

func assignmentExists(ctx context.Context, tx pgx.Tx, memberID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runner_job_assignments WHERE member_id = $1)`, memberID).Scan(&exists)
	return exists, err
}

func (s *pgStore) ApplyAssignmentReport(ctx context.Context, hostID string, report syncproto.AssignmentReport, sealDeadline time.Time) error {
	if report.AssignmentID == "" || report.MemberID == "" || report.RequestID == "" || report.JobID == "" {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var providerJobID int64
	var state, trustClass, scopeID, sourceGeneration, class string
	err = tx.QueryRow(ctx, `
SELECT a.provider_job_id, a.state, COALESCE(d.trust_class, ''),
       COALESCE(a.workspace_scope_id::text, ''), a.source_generation, m.runner_class
FROM runner_job_assignments a
JOIN runner_pool_members m ON m.member_id = a.member_id
LEFT JOIN github_provider_demands d ON d.provider_job_id = a.provider_job_id
WHERE a.assignment_id::text = $1 AND a.member_id = $2 AND a.host_id = $3
  AND a.request_id = $4 AND a.protocol_job_id = $5
FOR UPDATE OF a`, report.AssignmentID, report.MemberID, hostID, report.RequestID, report.JobID).
		Scan(&providerJobID, &state, &trustClass, &scopeID, &sourceGeneration, &class)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "completed" || state == "sealed" || state == "requeued" || state == "failed_closed" {
		return nil
	}
	timing, err := json.Marshal(report.Timing)
	if err != nil {
		return err
	}
	restore := syncproto.RestoreReport{}
	if report.Restore != nil {
		restore = *report.Restore
	}
	if _, err := tx.Exec(ctx, `
UPDATE runner_job_assignments SET
    restore_outcome = $2, restore_failure_class = $3,
    restore_failure_code = $4, process_invalidated = $5,
    reason = $6,
    timing_json = (
        SELECT COALESCE(jsonb_agg(point ORDER BY first_seen), '[]'::jsonb)
        FROM (
            SELECT point, min(ordinality) AS first_seen
            FROM jsonb_array_elements(timing_json || $7::jsonb)
                 WITH ORDINALITY AS observations(point, ordinality)
            GROUP BY point->>'source', point->>'boot_id', point->>'sequence', point
        ) AS unique_observations
    ),
    updated_at = now()
WHERE assignment_id::text = $1`, report.AssignmentID, restore.Outcome,
		restore.FailureClass, restore.FailureCode, restore.ProcessInvalidated,
		report.Reason, string(timing)); err != nil {
		return err
	}
	if restore.ProcessInvalidated && sourceGeneration != "" {
		if _, err := tx.Exec(ctx, `
UPDATE workspace_generations SET
    process_valid = false,
    process_invalidated_at = COALESCE(process_invalidated_at, now()),
    process_invalidation_class = CASE WHEN process_invalidation_class = '' THEN $2 ELSE process_invalidation_class END,
    process_invalidation_code = CASE WHEN process_invalidation_code = '' THEN $3 ELSE process_invalidation_code END,
    updated_at = now()
WHERE generation = $1`, sourceGeneration, restore.FailureClass, restore.FailureCode); err != nil {
			return err
		}
	}
	switch report.State {
	case syncproto.AssignmentObserved, syncproto.AssignmentBinding, syncproto.AssignmentAuthorizing, syncproto.AssignmentRunning:
		if _, err := tx.Exec(ctx, `UPDATE runner_job_assignments SET state = $2, updated_at = now() WHERE assignment_id::text = $1 AND state NOT IN ('completed','sealed','requeued','failed_closed')`, report.AssignmentID, string(report.State)); err != nil {
			return err
		}
		if report.State == syncproto.AssignmentRunning {
			if _, err := tx.Exec(ctx, `UPDATE github_job_intents SET state = 'running', updated_at = now() WHERE provider_job_id = $1`, providerJobID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE github_provider_demands SET state = 'assigned', updated_at = now() WHERE provider_job_id = $1 AND state IN ('demand_recorded','capacity_requested')`, providerJobID); err != nil {
				return err
			}
		}

	case syncproto.AssignmentExited:
		if report.ExitCode == 0 && report.Checkpoint != nil && scopeID != "" && trustClass == trustClassBranch {
			var generation string
			err := tx.QueryRow(ctx, `
INSERT INTO workspace_generations (
    generation, host_id, runner_class, state, scope_id, source_generation,
    process_digest, criu_version
) VALUES (gen_random_uuid()::text, $1, $2, 'candidate', $3::uuid, NULLIF($4,''), $5, $6)
RETURNING generation`, hostID, class, scopeID, sourceGeneration,
				report.Checkpoint.Digest, report.Checkpoint.Version).Scan(&generation)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
UPDATE runner_job_assignments
SET state = 'sealing', exit_code = 0, seal_generation = $2,
    checkpoint_digest = $3, checkpoint_version = $4,
    seal_deadline_at = $5, updated_at = now()
WHERE assignment_id::text = $1`, report.AssignmentID, generation,
				report.Checkpoint.Digest, report.Checkpoint.Version, sealDeadline); err != nil {
				return err
			}
		} else {
			if err := completeAssignmentTx(ctx, tx, report.AssignmentID, providerJobID, report.ExitCode); err != nil {
				return err
			}
		}

	case syncproto.AssignmentSealed:
		if report.SealedGeneration == "" || report.Checkpoint == nil {
			return nil
		}
		tag, err := tx.Exec(ctx, `
UPDATE workspace_generations
SET sealed_at = now(), process_digest = $2, criu_version = $3, updated_at = now()
WHERE generation = $1 AND state = 'candidate'`, report.SealedGeneration,
			report.Checkpoint.Digest, report.Checkpoint.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if _, err := tx.Exec(ctx, `UPDATE runner_job_assignments SET state = 'sealed', updated_at = now() WHERE assignment_id::text = $1 AND seal_generation = $2`, report.AssignmentID, report.SealedGeneration); err != nil {
				return err
			}
			if err := completeIntentTx(ctx, tx, providerJobID); err != nil {
				return err
			}
		}

	case syncproto.AssignmentCompleted:
		if err := completeAssignmentTx(ctx, tx, report.AssignmentID, providerJobID, report.ExitCode); err != nil {
			return err
		}

	case syncproto.AssignmentRequeued:
		if _, err := tx.Exec(ctx, `UPDATE runner_job_assignments SET state = 'requeued', reason = $2, updated_at = now() WHERE assignment_id::text = $1`, report.AssignmentID, report.Reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE github_job_intents SET state = 'requeued', request_id = '', protocol_job_id = '', updated_at = now() WHERE provider_job_id = $1`, providerJobID); err != nil {
			return err
		}

	case syncproto.AssignmentFailedClosed:
		if _, err := tx.Exec(ctx, `UPDATE runner_job_assignments SET state = 'failed_closed', reason = $2, updated_at = now() WHERE assignment_id::text = $1`, report.AssignmentID, report.Reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE github_job_intents SET state = 'failed_closed', updated_at = now() WHERE provider_job_id = $1`, providerJobID); err != nil {
			return err
		}
		if err := failDemandTx(ctx, tx, providerJobID, demandSandboxFailed,
			[]string{demandRecorded, demandCapacityRequested, demandAssigned},
			[]problem{problemSandboxFailed(report.Reason)}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func completeAssignmentTx(ctx context.Context, tx pgx.Tx, assignmentID string, providerJobID int64, exitCode int) error {
	if _, err := tx.Exec(ctx, `UPDATE runner_job_assignments SET state = 'completed', exit_code = $2, updated_at = now() WHERE assignment_id::text = $1`, assignmentID, exitCode); err != nil {
		return err
	}
	return completeIntentTx(ctx, tx, providerJobID)
}

func completeIntentTx(ctx context.Context, tx pgx.Tx, providerJobID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE github_job_intents SET state = 'completed', updated_at = now() WHERE provider_job_id = $1`, providerJobID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE github_provider_demands SET state = 'completed', updated_at = now() WHERE provider_job_id = $1 AND state NOT IN ('capacity_failed','jit_failed','sandbox_failed')`, providerJobID)
	return err
}

type desiredMemberRow struct {
	MemberID, VMID, RunnerName, RunnerClass, JITConfig, State string
}

func (s *pgStore) ListDesiredMembers(ctx context.Context, hostID string) ([]desiredMemberRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT member_id, vm_id, runner_name, runner_class, jit_config,
       CASE WHEN state = 'recycling' THEN 'recycle' ELSE 'listen' END
FROM runner_pool_members
WHERE host_id = $1 AND pool_id IS NOT NULL
  AND state NOT IN ('lost')
  AND (jit_config <> '' OR state = 'recycling')
ORDER BY member_id`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var desired []desiredMemberRow
	for rows.Next() {
		var row desiredMemberRow
		if err := rows.Scan(&row.MemberID, &row.VMID, &row.RunnerName, &row.RunnerClass, &row.JITConfig, &row.State); err != nil {
			return nil, err
		}
		desired = append(desired, row)
	}
	return desired, rows.Err()
}

type desiredAssignmentRow struct {
	AssignmentID, MemberID, RequestID, ProtocolJobID    string
	State                                               string
	ProviderJobID, InstallationID, RepositoryID         int64
	CheckRunID                                          int64
	RunID                                               string
	RunAttempt                                          int
	RunnerName, Repository, WorkflowJob                 string
	RunnerClass, ScopeGeneration                        string
	WorkspaceBytes, ToolBytes, ProcessBytes             int64
	ProcessDigest, ProcessVersion                       string
	SealGeneration, CheckpointDigest, CheckpointVersion string
}

func (s *pgStore) ListDesiredAssignments(ctx context.Context, hostID string) ([]desiredAssignmentRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT a.assignment_id::text, a.member_id, a.request_id, a.protocol_job_id, a.check_run_id, a.state,
       a.provider_job_id, d.provider_installation_id, d.provider_repository_id,
       a.run_id, a.run_attempt, a.runner_name, a.repository, a.workflow_job,
       m.runner_class, a.source_generation,
       c.disk_bytes, c.tool_disk_bytes, c.process_disk_bytes,
       a.source_process_digest, a.source_process_version,
       a.seal_generation, a.checkpoint_digest, a.checkpoint_version
FROM runner_job_assignments a
JOIN runner_pool_members m ON m.member_id = a.member_id
JOIN runner_classes c ON c.class = m.runner_class
JOIN github_provider_demands d ON d.provider_job_id = a.provider_job_id
WHERE a.host_id = $1 AND a.state IN
    ('observed','binding','authorizing','running','exited','sealing')
ORDER BY a.created_at`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var desired []desiredAssignmentRow
	for rows.Next() {
		var row desiredAssignmentRow
		if err := rows.Scan(
			&row.AssignmentID, &row.MemberID, &row.RequestID, &row.ProtocolJobID, &row.CheckRunID, &row.State,
			&row.ProviderJobID, &row.InstallationID, &row.RepositoryID,
			&row.RunID, &row.RunAttempt, &row.RunnerName, &row.Repository, &row.WorkflowJob,
			&row.RunnerClass, &row.ScopeGeneration,
			&row.WorkspaceBytes, &row.ToolBytes, &row.ProcessBytes,
			&row.ProcessDigest, &row.ProcessVersion,
			&row.SealGeneration, &row.CheckpointDigest, &row.CheckpointVersion,
		); err != nil {
			return nil, err
		}
		desired = append(desired, row)
	}
	return desired, rows.Err()
}

func (s *pgStore) ListReapGenerations(ctx context.Context, hostID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT generation FROM workspace_generations WHERE host_id = $1 AND state = 'reapable' ORDER BY generation`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var generations []string
	for rows.Next() {
		var generation string
		if err := rows.Scan(&generation); err != nil {
			return nil, err
		}
		generations = append(generations, generation)
	}
	return generations, rows.Err()
}

func (s *pgStore) ListHostPoolTargets(ctx context.Context, hostID string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
SELECT slots.class, LEAST(slots.total, COALESCE(sum(p.desired_count), 0))::integer
FROM host_slots slots
LEFT JOIN runner_pools p ON p.runner_class = slots.class AND p.enabled
WHERE slots.host_id = $1
GROUP BY slots.class, slots.total`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := map[string]int{}
	for rows.Next() {
		var class string
		var count int
		if err := rows.Scan(&class, &count); err != nil {
			return nil, err
		}
		targets[class] = count
	}
	return targets, rows.Err()
}

func (s *pgStore) ExpireSealingAssignments(ctx context.Context, now time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
UPDATE runner_job_assignments
SET state = 'completed', reason = 'snapshot seal not confirmed', updated_at = now()
WHERE state = 'sealing' AND seal_deadline_at <= $1
RETURNING seal_generation`, now)
	if err != nil {
		return 0, err
	}
	var generations []string
	for rows.Next() {
		var generation string
		if err := rows.Scan(&generation); err != nil {
			rows.Close()
			return 0, err
		}
		generations = append(generations, generation)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(generations) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE workspace_generations SET state = 'discarded', updated_at = now() WHERE generation = ANY($1::text[]) AND state = 'candidate'`, generations); err != nil {
			return 0, err
		}
	}
	return int64(len(generations)), tx.Commit(ctx)
}

func (s *pgStore) RecoverOfflineHosts(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
SELECT a.assignment_id::text, a.provider_job_id
FROM runner_job_assignments a
JOIN hosts h ON h.host_id = a.host_id
WHERE h.last_sync_at < $1
  AND a.state IN ('observed','binding','authorizing','running','exited')
FOR UPDATE OF a`, cutoff)
	if err != nil {
		return 0, err
	}
	var assignmentIDs []string
	var providerJobIDs []int64
	for rows.Next() {
		var assignmentID string
		var providerJobID int64
		if err := rows.Scan(&assignmentID, &providerJobID); err != nil {
			rows.Close()
			return 0, err
		}
		assignmentIDs = append(assignmentIDs, assignmentID)
		providerJobIDs = append(providerJobIDs, providerJobID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(assignmentIDs) > 0 {
		if _, err := tx.Exec(ctx, `
UPDATE runner_job_assignments
SET state = 'requeued', reason = 'host stopped syncing', updated_at = now()
WHERE assignment_id::text = ANY($1::text[])`, assignmentIDs); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
UPDATE github_job_intents
SET state = 'requeued', request_id = '', protocol_job_id = '', updated_at = now()
WHERE provider_job_id = ANY($1::bigint[])`, providerJobIDs); err != nil {
			return 0, err
		}
	}
	sealingRows, err := tx.Query(ctx, `
UPDATE runner_job_assignments a
SET state = 'completed', reason = 'generation seal abandoned after host loss', updated_at = now()
FROM hosts h
WHERE h.host_id = a.host_id AND h.last_sync_at < $1 AND a.state = 'sealing'
RETURNING a.provider_job_id, a.seal_generation`, cutoff)
	if err != nil {
		return 0, err
	}
	var sealedJobs []int64
	var abandonedGenerations []string
	for sealingRows.Next() {
		var providerJobID int64
		var generation string
		if err := sealingRows.Scan(&providerJobID, &generation); err != nil {
			sealingRows.Close()
			return 0, err
		}
		sealedJobs = append(sealedJobs, providerJobID)
		if generation != "" {
			abandonedGenerations = append(abandonedGenerations, generation)
		}
	}
	sealingRows.Close()
	if err := sealingRows.Err(); err != nil {
		return 0, err
	}
	if len(abandonedGenerations) > 0 {
		if _, err := tx.Exec(ctx, `
UPDATE workspace_generations
SET state = 'discarded', updated_at = now()
WHERE generation = ANY($1::text[]) AND state = 'candidate'`, abandonedGenerations); err != nil {
			return 0, err
		}
	}
	for _, providerJobID := range sealedJobs {
		if err := completeIntentTx(ctx, tx, providerJobID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE runner_pool_members m
SET state = 'lost', jit_config = '', reported_reason = 'host stopped syncing', updated_at = now()
FROM hosts h
WHERE h.host_id = m.host_id AND h.last_sync_at < $1
  AND m.state NOT IN ('lost', 'recycling')`, cutoff); err != nil {
		return 0, err
	}
	return int64(len(assignmentIDs) + len(sealedJobs)), tx.Commit(ctx)
}

type sealedCandidate struct {
	Generation, ScopeID, HostID, ObservedSource, AssignmentID, AssignmentAttemptID string
	ProviderJobID, JobRunAttempt                                                   int64
	JobConclusion                                                                  string
}

func (s *pgStore) ListSealedCandidates(ctx context.Context, batch int) ([]sealedCandidate, error) {
	rows, err := s.pool.Query(ctx, `
SELECT g.generation, g.scope_id::text, g.host_id, COALESCE(a.source_generation, ''),
       a.assignment_id::text, a.run_attempt::text, a.provider_job_id,
       j.provider_run_attempt, j.conclusion
FROM workspace_generations g
JOIN runner_job_assignments a ON a.seal_generation = g.generation
JOIN github_workflow_jobs j ON j.provider_job_id = a.provider_job_id
WHERE g.state = 'candidate' AND g.sealed_at IS NOT NULL AND g.scope_id IS NOT NULL
  AND j.status = 'completed' AND j.terminal_observed_from_api_at IS NOT NULL
ORDER BY g.updated_at LIMIT $1`, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []sealedCandidate
	for rows.Next() {
		var candidate sealedCandidate
		if err := rows.Scan(&candidate.Generation, &candidate.ScopeID, &candidate.HostID,
			&candidate.ObservedSource, &candidate.AssignmentID, &candidate.AssignmentAttemptID,
			&candidate.ProviderJobID, &candidate.JobRunAttempt, &candidate.JobConclusion); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// failDemandTx moves a demand to a terminal failure state and appends its
// structured problem history in the same transaction.
func failDemandTx(ctx context.Context, tx pgx.Tx, jobID int64, state string, from []string, problems []problem) error {
	for _, p := range problems {
		if _, err := tx.Exec(ctx, sqlAppendDemandProblem,
			jobID, phaseProcessing, p.typeURI(), p.Code, p.Title, p.Detail, p.Status, p.Retryable, p.Pointer); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE github_provider_demands SET state = $2, updated_at = now() WHERE provider_job_id = $1 AND state = ANY($3)`, jobID, state, from)
	return err
}
