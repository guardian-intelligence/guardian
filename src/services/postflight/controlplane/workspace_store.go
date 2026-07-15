package main

import (
	"context"
	"time"
)

// workspaceScopeKey is the job-shape identity of one workspace lineage.
// Every dimension comes from data the queued-job ingest already holds.
type workspaceScopeKey struct {
	Org          string
	Repo         string
	ScopeRef     string
	WorkflowPath string
	JobName      string
	MatrixKey    string
	RunnerClass  string
}

const sqlEnsureWorkspaceScope = `
INSERT INTO workspace_scopes (org, repo, scope_ref, workflow_path, job_name, matrix_key, runner_class)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (org, repo, scope_ref, workflow_path, job_name, matrix_key, runner_class)
DO UPDATE SET updated_at = now()
RETURNING scope_id::text`

// EnsureWorkspaceScope upserts the scope row for a job shape and returns its
// id. The no-op DO UPDATE makes the RETURNING fire on the existing row.
func (s *pgStore) EnsureWorkspaceScope(ctx context.Context, key workspaceScopeKey) (string, error) {
	var scopeID string
	err := s.pool.QueryRow(ctx, sqlEnsureWorkspaceScope,
		key.Org, key.Repo, key.ScopeRef, key.WorkflowPath, key.JobName, key.MatrixKey, key.RunnerClass,
	).Scan(&scopeID)
	return scopeID, err
}

// sealedCandidate is one host-confirmed seal whose GitHub verdict has been
// observed from the API: everything the promotion pass needs to classify it.
type sealedCandidate struct {
	Generation     string
	ScopeID        string
	HostID         string
	ObservedSource string // "" = the scope had no generation at claim
	LeaseID        string
	LeaseAttemptID string
	ProviderJobID  int64
	JobRunAttempt  int64
	JobConclusion  string
}

// sqlListSealedCandidates: promotion inputs. Both gates are deliberate —
// sealed_at proves the host confirmed the exact generation, and
// terminal_observed_from_api_at proves an API read (never a webhook hint)
// carried the completed status. A job not yet observed simply isn't listed:
// ambiguity never advances anything.
const sqlListSealedCandidates = `
SELECT g.generation, g.scope_id::text, g.host_id, COALESCE(l.observed_source_generation, ''),
    l.lease_id, l.attempt_id, l.provider_job_id, j.provider_run_attempt, j.conclusion
FROM workspace_generations g
JOIN host_leases l ON l.seal_generation = g.generation
JOIN github_workflow_jobs j ON j.provider_job_id = l.provider_job_id
WHERE g.state = 'candidate' AND g.sealed_at IS NOT NULL AND g.scope_id IS NOT NULL
  AND j.status = 'completed' AND j.terminal_observed_from_api_at IS NOT NULL
ORDER BY g.updated_at
LIMIT $1`

func (s *pgStore) ListSealedCandidates(ctx context.Context, batch int) ([]sealedCandidate, error) {
	rows, err := s.pool.Query(ctx, sqlListSealedCandidates, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sealedCandidate
	for rows.Next() {
		var c sealedCandidate
		if err := rows.Scan(&c.Generation, &c.ScopeID, &c.HostID, &c.ObservedSource,
			&c.LeaseID, &c.LeaseAttemptID, &c.ProviderJobID, &c.JobRunAttempt, &c.JobConclusion); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sqlPromoteScopePointer is THE compare-and-swap: the pointer advances only
// if it still holds the exact value this lease cloned from (NULL included,
// via IS NOT DISTINCT FROM — the cold-seed case). home_host_id follows the
// winner's residency.
const (
	sqlPromoteScopePointer = `
UPDATE workspace_scopes
SET current_generation_id = $2, home_host_id = $3, updated_at = now()
WHERE scope_id = $1::uuid AND current_generation_id IS NOT DISTINCT FROM NULLIF($4, '')`

	sqlCommitGeneration = `
UPDATE workspace_generations SET state = 'committed', updated_at = now()
WHERE generation = $1 AND state = 'candidate'`

	sqlRetainCandidate = `
UPDATE workspace_generations SET state = 'retained', updated_at = now()
WHERE generation = $1 AND state = 'candidate'`

	sqlRetireGeneration = `
UPDATE workspace_generations SET state = 'retained', updated_at = now()
WHERE generation = $1 AND state = 'committed'`
)

// PromoteGeneration runs one candidate's CAS. Winner: the pointer advances,
// the candidate commits, and the displaced predecessor is demoted to
// retained. Loser (something else advanced the pointer since this lease's
// claim): the candidate is retained — kept on disk until the retention
// sweep proves it unreferenced. The row locks taken by the CAS serialize
// concurrent promoters on the scope, so a raced duplicate promotion
// re-evaluates against the winner's pointer, loses, and its retain no-ops
// against the already-committed row (retained=false: nothing happened).
func (s *pgStore) PromoteGeneration(ctx context.Context, c sealedCandidate) (promoted, retained bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, sqlPromoteScopePointer, c.ScopeID, c.Generation, c.HostID, c.ObservedSource)
	if err != nil {
		return false, false, err
	}
	if tag.RowsAffected() == 0 {
		rtag, err := tx.Exec(ctx, sqlRetainCandidate, c.Generation)
		if err != nil {
			return false, false, err
		}
		return false, rtag.RowsAffected() > 0, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, sqlCommitGeneration, c.Generation); err != nil {
		return false, false, err
	}
	if c.ObservedSource != "" {
		if _, err := tx.Exec(ctx, sqlRetireGeneration, c.ObservedSource); err != nil {
			return false, false, err
		}
	}
	return true, false, tx.Commit(ctx)
}

// DiscardGeneration drops a candidate whose GitHub verdict was anything but
// an unambiguous attempt-matching success. The previous current stays
// authoritative.
func (s *pgStore) DiscardGeneration(ctx context.Context, generation string) (bool, error) {
	tag, err := s.pool.Exec(ctx, sqlDiscardGeneration, generation)
	return tag.RowsAffected() > 0, err
}

// sqlSweepReapableGenerations releases retained/discarded generations to the
// reap dispatch once nothing references them: not the scope pointer, not a
// pin, and not any live lease (as clone source, CAS guard, or pending seal
// target). The host additionally refuses to destroy a dataset with live
// clones, but the sweep is the invariant's owner.
const sqlSweepReapableGenerations = `
UPDATE workspace_generations g
SET state = 'reapable', updated_at = now()
WHERE g.state IN ('retained', 'discarded')
  AND NOT g.pinned
  AND NOT EXISTS (
      SELECT 1 FROM workspace_scopes s WHERE s.current_generation_id = g.generation)
  AND NOT EXISTS (
      SELECT 1 FROM host_leases l
      WHERE l.state IN ('allocating', 'assigned', 'ready', 'sealing')
        AND (l.workspace_generation = g.generation
          OR l.observed_source_generation = g.generation
          OR l.seal_generation = g.generation))`

func (s *pgStore) SweepReapableGenerations(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, sqlSweepReapableGenerations)
	return tag.RowsAffected(), err
}

// sqlDiscardStaleCandidates ages out candidates that have no other exit: a
// lost completed delivery means no API read ever observes the verdict (the
// missed-webhook reconciler only chases still-queued jobs), and an
// inventory-adopted row has no job at all — either would otherwise pin its
// dataset forever. Candidates still held by a sealing lease are excluded;
// the seal deadline owns those. Adopted rows (never sealed) age from
// creation.
const sqlDiscardStaleCandidates = `
UPDATE workspace_generations g
SET state = 'discarded', updated_at = now()
WHERE g.state = 'candidate'
  AND COALESCE(g.sealed_at, g.created_at) <= $1
  AND NOT EXISTS (
      SELECT 1 FROM host_leases l
      WHERE l.state = 'sealing' AND l.seal_generation = g.generation)`

func (s *pgStore) DiscardStaleCandidates(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, sqlDiscardStaleCandidates, cutoff)
	return tag.RowsAffected(), err
}
