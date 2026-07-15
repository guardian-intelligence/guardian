package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

// Control-plane lease states (host_leases.state). hostd's own lifecycle is
// finer-grained; the control plane only tracks the placement arc.
const (
	leaseAllocating = "allocating"
	leaseAssigned   = "assigned"
	leaseReady      = "ready"
	leaseSealing    = "sealing"
	leaseCompleted  = "completed"
	leaseFailed     = "failed"
	leaseExpired    = "expired"
)

// Workspace generation states (workspace_generations.state). The scope
// pointer only ever references a committed generation; candidates are on
// their way in, everything else is on its way out.
const (
	genCandidate = "candidate"
	genCommitted = "committed"
	genRetained  = "retained"
	genDiscarded = "discarded"
	genReapable  = "reapable"
	genReaped    = "reaped"
)

// Demand states the scheduler advances through (the full vocabulary is in
// 001_initial.sql's github_provider_demands comment).
const (
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
    boot_id      = EXCLUDED.boot_id,
    last_sync_at = now(),
    updated_at   = now()`

	sqlUpsertHostSlot = `
INSERT INTO host_slots (host_id, class, total, warm, used)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (host_id, class) DO UPDATE SET
    total      = EXCLUDED.total,
    warm       = EXCLUDED.warm,
    used       = EXCLUDED.used,
    updated_at = now()`

	sqlDeleteUnreportedSlots = `
DELETE FROM host_slots WHERE host_id = $1 AND class <> ALL($2::text[])`
)

// UpsertHostSync records one sync request's host identity and replaces the
// host's slot inventory (level-triggered full state; the control plane's
// reserved counter is preserved).
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
		if slot.Class == "" || slot.Total < 0 {
			continue
		}
		classes = append(classes, slot.Class)
		if _, err := tx.Exec(ctx, sqlUpsertHostSlot, hostID, slot.Class, slot.Total, slot.Warm, slot.Used); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, sqlDeleteUnreportedSlots, hostID, classes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// sqlObserveGeneration: residency and size follow the host's report; state,
// pinned, and last_used_at belong to the catalog's own lifecycle and are
// never touched by observation.
const sqlObserveGeneration = `
INSERT INTO workspace_generations (generation, host_id, bytes, state)
VALUES ($1, $2, $3, 'candidate')
ON CONFLICT (generation) DO UPDATE SET
    host_id    = EXCLUDED.host_id,
    bytes      = EXCLUDED.bytes,
    updated_at = now()`

// sqlConfirmReapedGenerations: the inventory report is full state, so a
// reapable generation the host no longer lists has actually been destroyed.
// Only 'reapable' rows transition on absence — anything else missing from a
// report is a catalog/host disagreement, not a confirmation.
const sqlConfirmReapedGenerations = `
UPDATE workspace_generations SET state = 'reaped', updated_at = now()
WHERE host_id = $1 AND state = 'reapable' AND generation <> ALL($2::text[])`

func (s *pgStore) ObserveHostGenerations(ctx context.Context, hostID string, generations []syncproto.GenerationReport) error {
	names := make([]string, 0, len(generations))
	for _, g := range generations {
		if g.Generation == "" {
			continue
		}
		names = append(names, g.Generation)
		if _, err := s.pool.Exec(ctx, sqlObserveGeneration, g.Generation, hostID, g.Bytes); err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, sqlConfirmReapedGenerations, hostID, names)
	return err
}

const sqlRecordLeaseReportedState = `
UPDATE host_leases SET reported_state = $3, updated_at = now()
WHERE lease_id = $1 AND host_id = $2 AND reported_state <> $3`

func (s *pgStore) RecordLeaseReportedState(ctx context.Context, hostID, leaseID, reported string) error {
	_, err := s.pool.Exec(ctx, sqlRecordLeaseReportedState, leaseID, hostID, reported)
	return err
}

const sqlMarkLeaseReady = `
UPDATE host_leases
SET state = 'ready', reported_state = 'ready', updated_at = now()
WHERE lease_id = $1 AND host_id = $2 AND state = 'assigned'`

func (s *pgStore) MarkLeaseReady(ctx context.Context, hostID, leaseID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, sqlMarkLeaseReady, leaseID, hostID)
	return tag.RowsAffected() > 0, err
}

// releaseHostSlot returns a claimed slot to the pool. GREATEST guards the
// counter against going negative if a host's slot row was replaced between
// claim and release.
const sqlReleaseHostSlot = `
UPDATE host_slots SET reserved = GREATEST(reserved - 1, 0), updated_at = now()
WHERE host_id = $1 AND class = $2`

const sqlAdvanceDemand = `
UPDATE github_provider_demands SET state = $2, updated_at = now()
WHERE provider_job_id = $1 AND state = ANY($3)`

// failDemandTx moves a demand to a terminal failure state (guarded by its
// current state) and appends the failure problems.
func failDemandTx(ctx context.Context, tx pgx.Tx, jobID int64, state string, from []string, problems []problem) error {
	for _, p := range problems {
		if _, err := tx.Exec(ctx, sqlAppendDemandProblem,
			jobID, phaseProcessing, p.typeURI(), p.Code, p.Title, p.Detail, p.Status, p.Retryable, p.Pointer); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, sqlAdvanceDemand, jobID, state, from)
	return err
}

// Terminal transitions scrub jit_config: the encoded runner registration
// credential must not accumulate at rest once the lease can no longer use it.
const sqlCompleteLease = `
UPDATE host_leases
SET state = 'completed', reported_state = 'exited', exit_code = $3, jit_config = '', updated_at = now()
WHERE lease_id = $1 AND host_id = $2 AND state IN ('assigned', 'ready')
RETURNING provider_job_id, runner_class`

// sqlLeaseExitContext reads what the exited-report transition needs to
// decide between plain completion and a seal request. No lock: trust class
// and scope are immutable after creation, and the guarded transition below
// is what enforces exactly-once.
const sqlLeaseExitContext = `
SELECT l.runner_class, COALESCE(l.workspace_scope_id::text, ''),
    COALESCE(l.observed_source_generation, ''), COALESCE(d.trust_class, '')
FROM host_leases l
LEFT JOIN github_provider_demands d ON d.provider_job_id = l.provider_job_id
WHERE l.lease_id = $1 AND l.host_id = $2 AND l.state IN ('assigned', 'ready')`

// sqlInsertSealGeneration mints the candidate the seal must produce; the id
// is a UUID so it is zfs-name safe on the host.
const sqlInsertSealGeneration = `
INSERT INTO workspace_generations (generation, host_id, runner_class, state, scope_id, source_generation)
VALUES (gen_random_uuid()::text, $1, $2, 'candidate', $3::uuid, NULLIF($4, ''))
RETURNING generation`

// sqlSealLease keeps the lease in the desired set as a seal request. The
// slot is still released here — occupancy never waits on the seal, let
// alone on GitHub.
const sqlSealLease = `
UPDATE host_leases
SET state = 'sealing', reported_state = 'exited', exit_code = $3, jit_config = '',
    seal_generation = $4, seal_deadline_at = $5, updated_at = now()
WHERE lease_id = $1 AND host_id = $2 AND state IN ('assigned', 'ready')
RETURNING provider_job_id, runner_class`

// CompleteLease is the exited-report transition: terminalize the lease (or,
// for a zero exit on a branch-trust lease with a scope, hold it in 'sealing'
// with a freshly minted candidate generation), free its slot, and complete
// the demand — one transaction, guarded by the lease's current state so a
// replayed report is a no-op. Only branch trust ever seals: PR workspaces
// are read-only borrowers of the target branch's lineage.
func (s *pgStore) CompleteLease(ctx context.Context, hostID, leaseID string, exitCode int, sealDeadline time.Time) (jobID int64, sealGeneration string, done bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", false, err
	}
	defer tx.Rollback(ctx)
	var class, scopeID, observedSource, trustClass string
	err = tx.QueryRow(ctx, sqlLeaseExitContext, leaseID, hostID).Scan(&class, &scopeID, &observedSource, &trustClass)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if exitCode == 0 && trustClass == trustClassBranch && scopeID != "" {
		if err := tx.QueryRow(ctx, sqlInsertSealGeneration, hostID, class, scopeID, observedSource).Scan(&sealGeneration); err != nil {
			return 0, "", false, err
		}
		err = tx.QueryRow(ctx, sqlSealLease, leaseID, hostID, exitCode, sealGeneration, sealDeadline).Scan(&jobID, &class)
	} else {
		err = tx.QueryRow(ctx, sqlCompleteLease, leaseID, hostID, exitCode).Scan(&jobID, &class)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if _, err := tx.Exec(ctx, sqlReleaseHostSlot, hostID, class); err != nil {
		return 0, "", false, err
	}
	if _, err := tx.Exec(ctx, sqlAdvanceDemand, jobID, demandCompleted,
		[]string{demandCapacityRequested, demandAssigned}); err != nil {
		return 0, "", false, err
	}
	return jobID, sealGeneration, true, tx.Commit(ctx)
}

const sqlRecordLeaseSealed = `
UPDATE host_leases
SET state = 'completed', reported_state = 'sealed', updated_at = now()
WHERE lease_id = $1 AND host_id = $2 AND state = 'sealing' AND seal_generation = $3
RETURNING provider_job_id`

const sqlMarkGenerationSealed = `
UPDATE workspace_generations SET sealed_at = now(), updated_at = now()
WHERE generation = $1 AND state = 'candidate'`

// RecordLeaseSealed is the sealed-report transition: the host confirmed the
// exact generation this lease was asked to produce, so the candidate becomes
// promotable and the lease terminalizes (leaving the desired set, which lets
// the host collect the workspace volume).
func (s *pgStore) RecordLeaseSealed(ctx context.Context, hostID, leaseID, sealedGeneration string) (int64, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	var jobID int64
	err = tx.QueryRow(ctx, sqlRecordLeaseSealed, leaseID, hostID, sealedGeneration).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, sqlMarkGenerationSealed, sealedGeneration); err != nil {
		return 0, false, err
	}
	return jobID, true, tx.Commit(ctx)
}

const sqlFailSealingLease = `
UPDATE host_leases
SET state = 'failed', reported_state = $3, reason = $4, updated_at = now()
WHERE lease_id = $1 AND host_id = $2 AND state = 'sealing'
RETURNING seal_generation`

const sqlDiscardGeneration = `
UPDATE workspace_generations SET state = 'discarded', updated_at = now()
WHERE generation = $1 AND state = 'candidate'`

// FailSealingLease handles a host-reported failure after the runner already
// exited green: the candidate is discarded and the lease terminalizes. The
// slot was released at the exited report and the demand already completed —
// a failed seal is a lost cache write, never a job result change.
func (s *pgStore) FailSealingLease(ctx context.Context, hostID, leaseID, reported, reason string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var sealGeneration string
	err = tx.QueryRow(ctx, sqlFailSealingLease, leaseID, hostID, reported, reason).Scan(&sealGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, sqlDiscardGeneration, sealGeneration); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

const sqlFailLeaseFromHost = `
UPDATE host_leases
SET state = 'failed', reported_state = $3, reason = $4, jit_config = '', updated_at = now()
WHERE lease_id = $1 AND host_id = $2 AND state IN ('assigned', 'ready')
RETURNING provider_job_id, runner_class`

// FailLeaseFromHost is the failed/cancelled-report transition: terminalize,
// free the slot, and fail the demand as sandbox_failed.
func (s *pgStore) FailLeaseFromHost(ctx context.Context, hostID, leaseID, reported, reason string, problems []problem) (int64, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	var jobID int64
	var class string
	err = tx.QueryRow(ctx, sqlFailLeaseFromHost, leaseID, hostID, reported, reason).Scan(&jobID, &class)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, sqlReleaseHostSlot, hostID, class); err != nil {
		return 0, false, err
	}
	if err := failDemandTx(ctx, tx, jobID, demandSandboxFailed,
		[]string{demandCapacityRequested, demandAssigned}, problems); err != nil {
		return 0, false, err
	}
	return jobID, true, tx.Commit(ctx)
}

// desiredLeaseRow is one lease as projected into a host's desired set.
type desiredLeaseRow struct {
	LeaseID            string
	State              string
	ExecutionID        string
	AttemptID          string
	OrgID              string
	InstallationID     int64
	RepositoryID       int64
	RepositoryFullName string
	RunnerClass        string
	JITConfig          string
	Generation         string
	SizeBytes          int64
	SealGeneration     string
}

const sqlListDesiredLeases = `
SELECT lease_id, state, execution_id, attempt_id, org_id, installation_id, repository_id,
    repository_full_name, runner_class, jit_config, workspace_generation, workspace_size_bytes,
    seal_generation
FROM host_leases
WHERE host_id = $1 AND state IN ('assigned', 'ready', 'sealing')
ORDER BY created_at, lease_id`

func (s *pgStore) ListDesiredLeases(ctx context.Context, hostID string) ([]desiredLeaseRow, error) {
	rows, err := s.pool.Query(ctx, sqlListDesiredLeases, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []desiredLeaseRow
	for rows.Next() {
		var r desiredLeaseRow
		if err := rows.Scan(&r.LeaseID, &r.State, &r.ExecutionID, &r.AttemptID, &r.OrgID, &r.InstallationID,
			&r.RepositoryID, &r.RepositoryFullName, &r.RunnerClass, &r.JITConfig,
			&r.Generation, &r.SizeBytes, &r.SealGeneration); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sqlListHostPoolTargets: warm VM ≡ slot, so every offered slot is a warm
// target; assigned VMs occupy slots and bound the governor's refill on the
// host side.
const sqlListHostPoolTargets = `
SELECT class, total FROM host_slots WHERE host_id = $1`

func (s *pgStore) ListHostPoolTargets(ctx context.Context, hostID string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, sqlListHostPoolTargets, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := map[string]int{}
	for rows.Next() {
		var class string
		var total int
		if err := rows.Scan(&class, &total); err != nil {
			return nil, err
		}
		targets[class] = total
	}
	return targets, rows.Err()
}

const sqlListReapGenerations = `
SELECT generation FROM workspace_generations
WHERE host_id = $1 AND state = 'reapable' AND NOT pinned
ORDER BY generation`

func (s *pgStore) ListReapGenerations(ctx context.Context, hostID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, sqlListReapGenerations, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// schedulableDemand is one recorded demand whose class the control plane
// serves (the runner_classes join is the class allowlist).
type schedulableDemand struct {
	ProviderJobID        int64
	ProviderRepositoryID int64
	RepositoryFullName   string
	ProviderRunAttempt   int64
	RunnerClass          string
	DiskBytes            int64
	WorkspaceScopeID     string
}

const sqlListSchedulableDemands = `
SELECT d.provider_job_id, d.provider_repository_id, d.repository_full_name,
    d.provider_run_attempt, d.runner_class, rc.disk_bytes,
    COALESCE(d.workspace_scope_id::text, '')
FROM github_provider_demands d
JOIN runner_classes rc ON rc.class = d.runner_class
WHERE d.state = 'demand_recorded'
ORDER BY d.created_at
LIMIT $1`

func (s *pgStore) ListSchedulableDemands(ctx context.Context, batch int) ([]schedulableDemand, error) {
	rows, err := s.pool.Query(ctx, sqlListSchedulableDemands, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schedulableDemand
	for rows.Next() {
		var d schedulableDemand
		if err := rows.Scan(&d.ProviderJobID, &d.ProviderRepositoryID, &d.RepositoryFullName,
			&d.ProviderRunAttempt, &d.RunnerClass, &d.DiskBytes, &d.WorkspaceScopeID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// unknownClassDemand is a recorded demand whose runner_class has no
// runner_classes row: the schedulable join above would filter it out
// silently forever, so the scheduler terminalizes it with a problem instead.
type unknownClassDemand struct {
	ProviderJobID      int64
	RepositoryFullName string
	RunnerClass        string
}

const sqlListUnknownClassDemands = `
SELECT d.provider_job_id, d.repository_full_name, d.runner_class
FROM github_provider_demands d
WHERE d.state = 'demand_recorded'
  AND NOT EXISTS (SELECT 1 FROM runner_classes rc WHERE rc.class = d.runner_class)
ORDER BY d.created_at
LIMIT $1`

func (s *pgStore) ListUnknownClassDemands(ctx context.Context, batch int) ([]unknownClassDemand, error) {
	rows, err := s.pool.Query(ctx, sqlListUnknownClassDemands, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []unknownClassDemand
	for rows.Next() {
		var d unknownClassDemand
		if err := rows.Scan(&d.ProviderJobID, &d.RepositoryFullName, &d.RunnerClass); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const sqlInsertLease = `
INSERT INTO host_leases (
    provider_job_id, execution_id, attempt_id, org_id, installation_id,
    repository_id, repository_full_name, runner_class, state,
    workspace_size_bytes, allocate_deadline_at, workspace_scope_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'allocating', $9, $10, NULLIF($11, '')::uuid)
RETURNING lease_id`

// CreateLeaseForDemand advances a recorded demand to capacity_requested and
// creates its allocating lease, atomically; created=false means another
// scheduler pass claimed the demand first.
func (s *pgStore) CreateLeaseForDemand(ctx context.Context, d schedulableDemand, executionID, attemptID, orgID string, installationID int64, allocateDeadline time.Time) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, sqlAdvanceDemand, d.ProviderJobID, demandCapacityRequested, []string{demandRecorded})
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() == 0 {
		return "", false, nil
	}
	var leaseID string
	if err := tx.QueryRow(ctx, sqlInsertLease,
		d.ProviderJobID, executionID, attemptID, orgID, installationID,
		d.ProviderRepositoryID, d.RepositoryFullName, d.RunnerClass,
		d.DiskBytes, allocateDeadline, d.WorkspaceScopeID).Scan(&leaseID); err != nil {
		return "", false, err
	}
	return leaseID, true, tx.Commit(ctx)
}

type allocatingLease struct {
	LeaseID       string
	ProviderJobID int64
	RunnerClass   string
}

const sqlListAllocatingLeases = `
SELECT lease_id, provider_job_id, runner_class
FROM host_leases
WHERE state = 'allocating'
ORDER BY created_at
LIMIT $1`

func (s *pgStore) ListAllocatingLeases(ctx context.Context, batch int) ([]allocatingLease, error) {
	rows, err := s.pool.Query(ctx, sqlListAllocatingLeases, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []allocatingLease
	for rows.Next() {
		var l allocatingLease
		if err := rows.Scan(&l.LeaseID, &l.ProviderJobID, &l.RunnerClass); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// sqlClaimHostSlot is the CAS slot claim: exactly one free slot on a
// recently-synced host offering the class is reserved — the lease's scope
// home host first (a clone is ~free where the source generation lives),
// then least-loaded. FOR UPDATE SKIP LOCKED makes concurrent claimers pick
// disjoint rows, and the reserved < total guard inside the locked pick makes
// double-assignment impossible. FOR UPDATE OF hs keeps the lock off the
// outer-joined scope row, which PostgreSQL would reject.
const sqlClaimHostSlot = `
UPDATE host_slots s
SET reserved = s.reserved + 1, updated_at = now()
FROM (
    SELECT hs.host_id, hs.class FROM host_slots hs
    JOIN hosts h ON h.host_id = hs.host_id
    LEFT JOIN host_leases cl ON cl.lease_id = $2
    LEFT JOIN workspace_scopes sc ON sc.scope_id = cl.workspace_scope_id
    WHERE hs.class = $1
      AND hs.reserved < hs.total
      AND h.last_sync_at > now() - interval '30 seconds'
    ORDER BY (hs.host_id = sc.home_host_id) DESC NULLS LAST, hs.reserved, hs.host_id
    FOR UPDATE OF hs SKIP LOCKED
    LIMIT 1
) pick
WHERE s.host_id = pick.host_id AND s.class = pick.class
RETURNING s.host_id`

// sqlBindLeaseHost stamps the claimed host onto the allocating lease in the
// claim's own transaction, together with the workspace this placement gets:
// observed_source_generation records the scope pointer as read right now
// (the promotion CAS guard), and workspace_generation is that pointer only
// when its generation is resident on the chosen host — anywhere else the
// clone source does not exist, so the lease runs cold (still correct; cache
// is acceleration). The host_id = ” guard makes the binding bind-once: a
// concurrent scheduler instance (deploy overlap) can never re-place a lease
// whose claim is mid-mint elsewhere.
const sqlBindLeaseHost = `
UPDATE host_leases l
SET host_id = $2,
    observed_source_generation = src.pointer,
    workspace_generation = COALESCE(src.resident, ''),
    updated_at = now()
FROM (
    SELECT sc.current_generation_id AS pointer,
        (SELECT g.generation FROM workspace_generations g
         WHERE g.generation = sc.current_generation_id AND g.host_id = $2) AS resident
    FROM host_leases cl
    LEFT JOIN workspace_scopes sc ON sc.scope_id = cl.workspace_scope_id
    WHERE cl.lease_id = $1
) src
WHERE l.lease_id = $1 AND l.state = 'allocating' AND l.host_id = ''`

// sqlLockLeaseScope share-locks the lease's scope row for the rest of the
// claim transaction. The promotion CAS updates that row, so it cannot commit
// — and retire the generation the bind below is about to read as the scope
// pointer — until this transaction's lease references are visible. Without
// the lock, a promotion plus a retention sweep landing inside the claim's
// commit window could mark the clone source reapable while no committed row
// references it yet.
const sqlLockLeaseScope = `
SELECT 1 FROM host_leases cl
JOIN workspace_scopes sc ON sc.scope_id = cl.workspace_scope_id
WHERE cl.lease_id = $1
FOR SHARE OF sc`

// sqlTouchCloneSource stamps last_used_at on the generation the bound lease
// clones (the retention policy's recency input); a cold bind touches nothing.
const sqlTouchCloneSource = `
UPDATE workspace_generations g
SET last_used_at = now(), updated_at = now()
FROM host_leases l
WHERE l.lease_id = $1 AND l.workspace_generation = g.generation`

// ClaimHostSlot reserves one free slot for the lease and binds the lease to
// the chosen host, atomically. The bound lease row is what carries the
// reservation through the JIT mint: every observer (the reconcile sweep, a
// second control-plane instance) sees the claim as lease truth, and a crash
// after the claim leaves a bound allocating lease whose allocate-deadline
// expiry releases the slot.
func (s *pgStore) ClaimHostSlot(ctx context.Context, leaseID, class string) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	hostID, claimed, err := claimHostSlotTx(ctx, tx, leaseID, class)
	if err != nil || !claimed {
		return "", false, err
	}
	return hostID, true, tx.Commit(ctx)
}

func claimHostSlotTx(ctx context.Context, tx pgx.Tx, leaseID, class string) (string, bool, error) {
	var hostID string
	err := tx.QueryRow(ctx, sqlClaimHostSlot, class, leaseID).Scan(&hostID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, sqlLockLeaseScope, leaseID); err != nil {
		return "", false, err
	}
	tag, err := tx.Exec(ctx, sqlBindLeaseHost, leaseID, hostID)
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() == 0 {
		// The lease left 'allocating' (or was bound elsewhere) concurrently;
		// the rollback discards the reservation.
		return "", false, nil
	}
	if _, err := tx.Exec(ctx, sqlTouchCloneSource, leaseID); err != nil {
		return "", false, err
	}
	return hostID, true, nil
}

// sqlReconcileSlotReservations resets every slot row's reserved counter to
// the count of leases actually holding a claim: assigned/ready leases plus
// allocating leases bound to the host (a claim mid-JIT-mint). Because the
// claim binds the lease in the same transaction as the counter bump, this
// sweep can run at any time — including from an overlapping control-plane
// instance — without erasing an in-flight claim; it only repairs genuine
// counter drift (a host slot row replaced mid-lease, a double release).
const sqlReconcileSlotReservations = `
UPDATE host_slots s
SET reserved = active.count, updated_at = now()
FROM (
    SELECT s2.host_id, s2.class,
        (SELECT COUNT(*) FROM host_leases l
         WHERE l.host_id = s2.host_id AND l.runner_class = s2.class
           AND l.state IN ('allocating', 'assigned', 'ready')) AS count
    FROM host_slots s2
) active
WHERE s.host_id = active.host_id AND s.class = active.class
  AND s.reserved <> active.count`

func (s *pgStore) ReconcileSlotReservations(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, sqlReconcileSlotReservations)
	return tag.RowsAffected(), err
}

const sqlAssignLease = `
UPDATE host_leases
SET state = 'assigned', jit_config = $3,
    assignment_deadline_at = $4, updated_at = now()
WHERE lease_id = $1 AND state = 'allocating' AND host_id = $2
RETURNING provider_job_id`

// AssignLease advances a claimed (host-bound) lease to assigned with the
// minted JIT config; assigned=false means the lease left 'allocating'
// concurrently — whichever transition took it released the claimed slot.
func (s *pgStore) AssignLease(ctx context.Context, leaseID, hostID, jitConfig string, assignmentDeadline time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var jobID int64
	err = tx.QueryRow(ctx, sqlAssignLease, leaseID, hostID, jitConfig, assignmentDeadline).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, sqlAdvanceDemand, jobID, demandAssigned, []string{demandCapacityRequested}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

const sqlFailAllocatingLease = `
UPDATE host_leases
SET state = 'failed', reason = $2, jit_config = '', updated_at = now()
WHERE lease_id = $1 AND state = 'allocating'
RETURNING provider_job_id, host_id, runner_class`

// FailAllocatingLease terminalizes a lease that never reached its host (JIT
// mint failure), releases the claimed slot if one is bound, and fails the
// demand as jit_failed — one transaction.
func (s *pgStore) FailAllocatingLease(ctx context.Context, leaseID, reason string, problems []problem) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var jobID int64
	var hostID, class string
	err = tx.QueryRow(ctx, sqlFailAllocatingLease, leaseID, reason).Scan(&jobID, &hostID, &class)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hostID != "" {
		if _, err := tx.Exec(ctx, sqlReleaseHostSlot, hostID, class); err != nil {
			return false, err
		}
	}
	if err := failDemandTx(ctx, tx, jobID, demandJITFailed,
		[]string{demandRecorded, demandCapacityRequested}, problems); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type overdueLease struct {
	LeaseID       string
	ProviderJobID int64
	HostID        string
	RunnerClass   string
	State         string
}

// sqlListOverdueLeases sweeps the ways a lease can be stuck: past its
// allocate deadline, past its assignment deadline, ready on a host that
// stopped syncing (a ready lease has no control-plane deadline of its own —
// hostd's ready bound only fires if the host is alive, so host death is the
// absence this sweep must observe), or sealing past its deadline or on a
// dead host.
const sqlListOverdueLeases = `
SELECT l.lease_id, l.provider_job_id, l.host_id, l.runner_class, l.state
FROM host_leases l
WHERE (l.state = 'allocating' AND l.allocate_deadline_at <= now())
   OR (l.state = 'assigned' AND l.assignment_deadline_at <= now())
   OR (l.state = 'ready' AND EXISTS (
        SELECT 1 FROM hosts h
        WHERE h.host_id = l.host_id AND h.last_sync_at <= $2))
   OR (l.state = 'sealing' AND (l.seal_deadline_at <= now() OR EXISTS (
        SELECT 1 FROM hosts h
        WHERE h.host_id = l.host_id AND h.last_sync_at <= $2)))
ORDER BY l.updated_at
LIMIT $1`

func (s *pgStore) ListOverdueLeases(ctx context.Context, batch int, hostDeadCutoff time.Time) ([]overdueLease, error) {
	rows, err := s.pool.Query(ctx, sqlListOverdueLeases, batch, hostDeadCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []overdueLease
	for rows.Next() {
		var l overdueLease
		if err := rows.Scan(&l.LeaseID, &l.ProviderJobID, &l.HostID, &l.RunnerClass, &l.State); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

const sqlExpireLease = `
UPDATE host_leases
SET state = 'expired', reason = $3, jit_config = '', updated_at = now()
WHERE lease_id = $1 AND state = $2
RETURNING host_id, runner_class, provider_job_id`

const sqlExpireSealingLease = `
UPDATE host_leases
SET state = 'expired', reason = $2, updated_at = now()
WHERE lease_id = $1 AND state = 'sealing'
RETURNING seal_generation`

// expireSealingLease terminalizes a seal that was never confirmed and
// discards its candidate. The slot was released and the demand completed at
// the exited report, so neither is touched: an unconfirmed seal costs only
// the cache write.
func (s *pgStore) expireSealingLease(ctx context.Context, l overdueLease, reason string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var sealGeneration string
	err = tx.QueryRow(ctx, sqlExpireSealingLease, l.LeaseID, reason).Scan(&sealGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, sqlDiscardGeneration, sealGeneration); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ExpireLease terminalizes a stuck lease: the guarded transition, the slot
// release (any lease bound to a host holds a reservation, including a claimed
// allocating lease orphaned mid-mint), and the demand failure land in one
// transaction.
func (s *pgStore) ExpireLease(ctx context.Context, l overdueLease, reason string, problems []problem) (bool, error) {
	if l.State == leaseSealing {
		return s.expireSealingLease(ctx, l, reason)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var hostID, class string
	var jobID int64
	err = tx.QueryRow(ctx, sqlExpireLease, l.LeaseID, l.State, reason).Scan(&hostID, &class, &jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hostID != "" {
		if _, err := tx.Exec(ctx, sqlReleaseHostSlot, hostID, class); err != nil {
			return false, err
		}
	}
	demandState := demandCapacityFailed
	demandFrom := []string{demandRecorded, demandCapacityRequested}
	if l.State == leaseAssigned || l.State == leaseReady {
		demandState = demandSandboxFailed
		demandFrom = []string{demandCapacityRequested, demandAssigned}
	}
	if err := failDemandTx(ctx, tx, jobID, demandState, demandFrom, problems); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
