package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Delivery ledger states: accepted -> processing -> {processed, ignored,
// retryable, failed}; rejected is the pre-verification terminal (and the only
// state a redelivery may resurrect). The SQL below spells the full
// vocabulary; only the states Go branches on get named constants.
const (
	stateAccepted  = "accepted"
	stateProcessed = "processed"
	stateRetryable = "retryable"
)

type deliveryEnvelope struct {
	DeliveryID             string
	EventName              string
	Action                 string
	PayloadSHA256          string
	PayloadJSON            []byte
	ProviderInstallationID int64
	ProviderRepositoryID   int64
	RepositoryFullName     string
	ProviderRunID          int64
	ProviderRunAttempt     int64
	ProviderJobID          int64
	ReceivedAt             time.Time
}

type deliveryAck struct {
	DeliveryID    string
	State         string
	AttemptCount  int32
	PayloadSHA256 string
}

// inboxStore is the narrow seam the webhook handler consumes; tests use a
// fake, production uses *pgStore. The worker and comment loop use *pgStore
// directly.
type inboxStore interface {
	RecordWebhookDelivery(ctx context.Context, env deliveryEnvelope) (deliveryAck, error)
	RecordRejectedDelivery(ctx context.Context, env deliveryEnvelope, problems []problem) error
}

// querier is satisfied by *pgxpool.Pool and pgx.Tx so problem appends can run
// inside transition transactions.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type pgStore struct {
	pool *pgxpool.Pool
}

func (s *pgStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// sqlRecordWebhookDelivery: insert as accepted, ready immediately. On a
// delivery-id conflict, every column is resurrected from EXCLUDED only when
// the existing row is 'rejected' (a transient rejection followed by GitHub
// manual redelivery self-heals); any other state is a no-op returning the
// existing row. The WHERE clause means a conflicting row with a DIFFERENT
// payload hash matches zero rows -> pgx.ErrNoRows -> the handler's 409
// replay-conflict security event. The stored delivery is never overwritten.
const sqlRecordWebhookDelivery = `
INSERT INTO github_webhook_deliveries (
    delivery_id, event_name, action, state, payload_sha256, payload_json,
    provider_installation_id, provider_repository_id, repository_full_name,
    provider_run_id, provider_run_attempt, provider_job_id,
    received_at, verified_at, next_attempt_at
) VALUES ($1, $2, $3, 'accepted', $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, $12, $12)
ON CONFLICT (delivery_id) DO UPDATE SET
    state                    = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN 'accepted' ELSE github_webhook_deliveries.state END,
    event_name               = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.event_name ELSE github_webhook_deliveries.event_name END,
    action                   = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.action ELSE github_webhook_deliveries.action END,
    payload_json             = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.payload_json ELSE github_webhook_deliveries.payload_json END,
    primary_problem_type     = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN '' ELSE github_webhook_deliveries.primary_problem_type END,
    primary_problem_code     = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN '' ELSE github_webhook_deliveries.primary_problem_code END,
    primary_problem_title    = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN '' ELSE github_webhook_deliveries.primary_problem_title END,
    primary_problem_detail   = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN '' ELSE github_webhook_deliveries.primary_problem_detail END,
    primary_problem_docs_url = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN '' ELSE github_webhook_deliveries.primary_problem_docs_url END,
    primary_problem_status   = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN 0 ELSE github_webhook_deliveries.primary_problem_status END,
    problem_count            = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN 0 ELSE github_webhook_deliveries.problem_count END,
    attempt_count            = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN 0 ELSE github_webhook_deliveries.attempt_count END,
    provider_installation_id = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.provider_installation_id ELSE github_webhook_deliveries.provider_installation_id END,
    provider_repository_id   = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.provider_repository_id ELSE github_webhook_deliveries.provider_repository_id END,
    repository_full_name     = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.repository_full_name ELSE github_webhook_deliveries.repository_full_name END,
    provider_run_id          = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.provider_run_id ELSE github_webhook_deliveries.provider_run_id END,
    provider_run_attempt     = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.provider_run_attempt ELSE github_webhook_deliveries.provider_run_attempt END,
    provider_job_id          = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.provider_job_id ELSE github_webhook_deliveries.provider_job_id END,
    received_at              = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.received_at ELSE github_webhook_deliveries.received_at END,
    verified_at              = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.verified_at ELSE github_webhook_deliveries.verified_at END,
    next_attempt_at          = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN EXCLUDED.next_attempt_at ELSE github_webhook_deliveries.next_attempt_at END,
    processing_started_at    = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN NULL ELSE github_webhook_deliveries.processing_started_at END,
    processed_at             = CASE WHEN github_webhook_deliveries.state = 'rejected' THEN NULL ELSE github_webhook_deliveries.processed_at END,
    updated_at               = now()
WHERE github_webhook_deliveries.payload_sha256 = EXCLUDED.payload_sha256
RETURNING delivery_id, state, attempt_count, payload_sha256`

func (s *pgStore) RecordWebhookDelivery(ctx context.Context, env deliveryEnvelope) (deliveryAck, error) {
	var ack deliveryAck
	err := s.pool.QueryRow(ctx, sqlRecordWebhookDelivery,
		env.DeliveryID, env.EventName, env.Action, env.PayloadSHA256, string(env.PayloadJSON),
		env.ProviderInstallationID, env.ProviderRepositoryID, env.RepositoryFullName,
		env.ProviderRunID, env.ProviderRunAttempt, env.ProviderJobID, env.ReceivedAt,
	).Scan(&ack.DeliveryID, &ack.State, &ack.AttemptCount, &ack.PayloadSHA256)
	return ack, err
}

// sqlInsertRejectedDelivery: a rejected delivery stores only its payload hash
// and problems, never the raw body ('{}' placeholder). On conflict, only an
// already-rejected row with a matching hash is re-touched; zero rows (healthy
// delivery, or hash mismatch) means the request problems are NOT attached.
const sqlInsertRejectedDelivery = `
INSERT INTO github_webhook_deliveries (
    delivery_id, event_name, action, state, payload_sha256, payload_json, received_at, verified_at
) VALUES ($1, $2, $3, 'rejected', $4, '{}'::jsonb, $5, $5)
ON CONFLICT (delivery_id) DO UPDATE SET updated_at = now()
WHERE github_webhook_deliveries.state = 'rejected'
  AND github_webhook_deliveries.payload_sha256 = EXCLUDED.payload_sha256
RETURNING delivery_id`

func (s *pgStore) RecordRejectedDelivery(ctx context.Context, env deliveryEnvelope, problems []problem) error {
	eventName := env.EventName
	if eventName == "" {
		// The X-GitHub-Event header itself may be the problem; the ledger
		// still wants a row (event_name has a non-empty CHECK).
		eventName = "unknown"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after commit

	var id string
	err = tx.QueryRow(ctx, sqlInsertRejectedDelivery,
		env.DeliveryID, eventName, env.Action, env.PayloadSHA256, env.ReceivedAt,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// The GUID exists in a non-rejected state or with a different hash:
		// never attach request problems to a healthy delivery.
		return nil
	}
	if err != nil {
		return err
	}
	for _, p := range problems {
		if err := appendDeliveryProblem(ctx, tx, id, phaseRequest, p); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// sqlAppendDeliveryProblem: append-only problem history plus primary-problem
// denormalization on the parent — the FIRST problem wins as primary
// (problem_count = 0 guard), later ones only bump the count.
const sqlAppendDeliveryProblem = `
WITH next_seq AS (
    SELECT COALESCE(MAX(problem_seq), 0) + 1 AS seq
    FROM github_webhook_delivery_problems
    WHERE delivery_id = $1
), ins AS (
    INSERT INTO github_webhook_delivery_problems (
        delivery_id, problem_seq, phase, problem_type, problem_code,
        title, detail, docs_url, status, retryable, pointer, observed_at
    )
    SELECT $1, seq, $2, $3, $4, $5, $6, '', $7, $8, $9, now() FROM next_seq
    RETURNING 1
)
UPDATE github_webhook_deliveries d SET
    primary_problem_type   = CASE WHEN d.problem_count = 0 THEN $3 ELSE d.primary_problem_type END,
    primary_problem_code   = CASE WHEN d.problem_count = 0 THEN $4 ELSE d.primary_problem_code END,
    primary_problem_title  = CASE WHEN d.problem_count = 0 THEN $5 ELSE d.primary_problem_title END,
    primary_problem_detail = CASE WHEN d.problem_count = 0 THEN $6 ELSE d.primary_problem_detail END,
    primary_problem_status = CASE WHEN d.problem_count = 0 THEN $7 ELSE d.primary_problem_status END,
    problem_count          = d.problem_count + (SELECT COUNT(*) FROM ins),
    updated_at             = now()
WHERE d.delivery_id = $1`

func appendDeliveryProblem(ctx context.Context, q querier, deliveryID, phase string, p problem) error {
	_, err := q.Exec(ctx, sqlAppendDeliveryProblem,
		deliveryID, phase, p.typeURI(), p.Code, p.Title, p.Detail, p.Status, p.Retryable, p.Pointer)
	return err
}

type lockedDelivery struct {
	DeliveryID             string
	EventName              string
	Action                 string
	PayloadSHA256          string
	PayloadJSON            []byte
	AttemptCount           int32
	ProviderInstallationID int64
	ProviderRepositoryID   int64
	RepositoryFullName     string
	ProviderRunID          int64
	ProviderRunAttempt     int64
	ProviderJobID          int64
	ReceivedAt             time.Time
}

const sqlLockReadyDeliveries = `
UPDATE github_webhook_deliveries d SET
    state = 'processing',
    attempt_count = d.attempt_count + 1,
    processing_started_at = now(),
    updated_at = now()
WHERE d.delivery_id IN (
    SELECT delivery_id FROM github_webhook_deliveries
    WHERE state IN ('accepted', 'retryable')
      AND (next_attempt_at IS NULL OR next_attempt_at <= now())
    ORDER BY received_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING d.delivery_id, d.event_name, d.action, d.payload_sha256, d.payload_json::text,
    d.attempt_count, d.provider_installation_id, d.provider_repository_id,
    d.repository_full_name, d.provider_run_id, d.provider_run_attempt,
    d.provider_job_id, d.received_at`

func (s *pgStore) LockReadyDeliveries(ctx context.Context, batch int) ([]lockedDelivery, error) {
	rows, err := s.pool.Query(ctx, sqlLockReadyDeliveries, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lockedDelivery
	for rows.Next() {
		var d lockedDelivery
		var payload string
		if err := rows.Scan(&d.DeliveryID, &d.EventName, &d.Action, &d.PayloadSHA256, &payload,
			&d.AttemptCount, &d.ProviderInstallationID, &d.ProviderRepositoryID,
			&d.RepositoryFullName, &d.ProviderRunID, &d.ProviderRunAttempt,
			&d.ProviderJobID, &d.ReceivedAt); err != nil {
			return nil, err
		}
		d.PayloadJSON = []byte(payload)
		out = append(out, d)
	}
	return out, rows.Err()
}

type staleDelivery struct {
	DeliveryID    string
	EventName     string
	AttemptCount  int32
	ProviderJobID int64
}

const sqlListStaleDeliveries = `
SELECT delivery_id, event_name, attempt_count, provider_job_id
FROM github_webhook_deliveries
WHERE state = 'processing' AND processing_started_at <= now() - make_interval(secs => $2)
ORDER BY processing_started_at
LIMIT $1`

func (s *pgStore) ListStaleDeliveries(ctx context.Context, batch int, staleAfter time.Duration) ([]staleDelivery, error) {
	rows, err := s.pool.Query(ctx, sqlListStaleDeliveries, batch, staleAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []staleDelivery
	for rows.Next() {
		var d staleDelivery
		if err := rows.Scan(&d.DeliveryID, &d.EventName, &d.AttemptCount, &d.ProviderJobID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// All transitions are guarded by state = 'processing' so a reclaimed or
// concurrently-moved delivery cannot be double-transitioned.
const (
	sqlMarkDeliveryProcessed = `
UPDATE github_webhook_deliveries
SET state = 'processed', processed_at = now(), processing_started_at = NULL, updated_at = now()
WHERE delivery_id = $1 AND state = 'processing'`

	sqlMarkDeliveryIgnored = `
UPDATE github_webhook_deliveries
SET state = 'ignored', processed_at = now(), processing_started_at = NULL, updated_at = now()
WHERE delivery_id = $1 AND state = 'processing'`

	sqlMarkDeliveryRetryable = `
UPDATE github_webhook_deliveries
SET state = 'retryable', next_attempt_at = $2, processing_started_at = NULL, updated_at = now()
WHERE delivery_id = $1 AND state = 'processing'`

	sqlMarkDeliveryFailed = `
UPDATE github_webhook_deliveries
SET state = 'failed', processed_at = now(), processing_started_at = NULL, updated_at = now()
WHERE delivery_id = $1 AND state = 'processing'`
)

// transitionDelivery is postflight's updateDeliveryWithProblems: one tx =
// problem appends + guarded state UPDATE.
func (s *pgStore) transitionDelivery(ctx context.Context, deliveryID, sql string, problems []problem, args ...any) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range problems {
		if err := appendDeliveryProblem(ctx, tx, deliveryID, phaseProcessing, p); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, sql, append([]any{deliveryID}, args...)...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *pgStore) MarkDeliveryProcessed(ctx context.Context, deliveryID string) error {
	return s.transitionDelivery(ctx, deliveryID, sqlMarkDeliveryProcessed, nil)
}

func (s *pgStore) MarkDeliveryIgnored(ctx context.Context, deliveryID string, problems []problem) error {
	return s.transitionDelivery(ctx, deliveryID, sqlMarkDeliveryIgnored, problems)
}

func (s *pgStore) MarkDeliveryRetryable(ctx context.Context, deliveryID string, nextAttempt time.Time, problems []problem) error {
	return s.transitionDelivery(ctx, deliveryID, sqlMarkDeliveryRetryable, problems, nextAttempt)
}

func (s *pgStore) MarkDeliveryFailed(ctx context.Context, deliveryID string, problems []problem) error {
	return s.transitionDelivery(ctx, deliveryID, sqlMarkDeliveryFailed, problems)
}

type workflowJobRow struct {
	ProviderJobID          int64
	ProviderInstallationID int64
	ProviderRunID          int64
	ProviderRunAttempt     int64
	ProviderRepositoryID   int64
	RepositoryFullName     string
	Name                   string
	Status                 string
	Conclusion             string
	Labels                 []string
	RunnerClass            string
	RunnerID               int64
	RunnerName             string
	HeadSHA                string
	HeadBranch             string
	WorkflowName           string
	StartedAt              *time.Time
	CompletedAt            *time.Time
	ObservedFromAPIAt      *time.Time // nil for webhook-hint writes
}

// sqlUpsertWorkflowJob is last-write-wins on all provider fields;
// observed_from_api_at is only ever advanced (an API truth never loses its
// provenance to a later webhook hint), terminal_observed_from_api_at is set
// only by API reads that carried the completed status (promotion's truth
// gate), and pr_number is owned by SetRunPullRequest.
const sqlUpsertWorkflowJob = `
INSERT INTO github_workflow_jobs (
    provider_job_id, provider_installation_id, provider_run_id, provider_run_attempt, provider_repository_id,
    repository_full_name, name, status, conclusion, labels_json, runner_class,
    runner_id, runner_name, head_sha, head_branch, workflow_name,
    started_at, completed_at, observed_from_api_at, terminal_observed_from_api_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13, $14, $15, $16, $17, $18, $19,
    CASE WHEN $19::timestamptz IS NOT NULL AND $8 = 'completed' THEN $19::timestamptz END)
ON CONFLICT (provider_job_id) DO UPDATE SET
    provider_installation_id = EXCLUDED.provider_installation_id,
    provider_run_id        = EXCLUDED.provider_run_id,
    provider_run_attempt   = EXCLUDED.provider_run_attempt,
    provider_repository_id = EXCLUDED.provider_repository_id,
    repository_full_name   = EXCLUDED.repository_full_name,
    name                   = EXCLUDED.name,
    status                 = EXCLUDED.status,
    conclusion             = EXCLUDED.conclusion,
    labels_json            = EXCLUDED.labels_json,
    runner_class           = EXCLUDED.runner_class,
    runner_id              = EXCLUDED.runner_id,
    runner_name            = EXCLUDED.runner_name,
    head_sha               = EXCLUDED.head_sha,
    head_branch            = EXCLUDED.head_branch,
    workflow_name          = EXCLUDED.workflow_name,
    started_at             = EXCLUDED.started_at,
    completed_at           = EXCLUDED.completed_at,
    observed_from_api_at   = COALESCE(EXCLUDED.observed_from_api_at, github_workflow_jobs.observed_from_api_at),
    terminal_observed_from_api_at = COALESCE(EXCLUDED.terminal_observed_from_api_at, github_workflow_jobs.terminal_observed_from_api_at),
    updated_at             = now()`

func (s *pgStore) UpsertWorkflowJob(ctx context.Context, j workflowJobRow) error {
	labels, err := json.Marshal(j.Labels)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, sqlUpsertWorkflowJob,
		j.ProviderJobID, j.ProviderInstallationID, j.ProviderRunID, j.ProviderRunAttempt, j.ProviderRepositoryID,
		j.RepositoryFullName, j.Name, j.Status, j.Conclusion, string(labels), j.RunnerClass,
		j.RunnerID, j.RunnerName, j.HeadSHA, j.HeadBranch, j.WorkflowName,
		j.StartedAt, j.CompletedAt, j.ObservedFromAPIAt)
	return err
}

const sqlSetRunPullRequest = `
UPDATE github_workflow_jobs SET pr_number = $2, updated_at = now()
WHERE provider_run_id = $1`

// SetRunPullRequest stamps the run's resolved PR number (0 = no PR) onto all
// of the run's job rows; call it after the run's jobs are upserted.
func (s *pgStore) SetRunPullRequest(ctx context.Context, runID, prNumber int64) error {
	_, err := s.pool.Exec(ctx, sqlSetRunPullRequest, runID, prNumber)
	return err
}

type demandRow struct {
	ProviderJobID          int64
	ProviderInstallationID int64
	ProviderRepositoryID   int64
	RepositoryFullName     string
	ProviderRunID          int64
	ProviderRunAttempt     int64
	TrustClass             string
	RunnerClass            string
	WorkspaceScopeID       string
	LastDeliveryID         string
}

// sqlEnsureProviderDemand refreshes envelope fields on conflict but NEVER
// touches state: a redelivered webhook cannot reset a demand's progress.
const sqlEnsureProviderDemand = `
INSERT INTO github_provider_demands (
    provider_job_id, provider_installation_id, provider_repository_id, repository_full_name,
    provider_run_id, provider_run_attempt, trust_class, runner_class,
    workspace_scope_id, state, last_delivery_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid, 'demand_recorded', $10)
ON CONFLICT (provider_job_id) DO UPDATE SET
    provider_installation_id = EXCLUDED.provider_installation_id,
    provider_repository_id = EXCLUDED.provider_repository_id,
    repository_full_name   = EXCLUDED.repository_full_name,
    provider_run_id        = EXCLUDED.provider_run_id,
    provider_run_attempt   = EXCLUDED.provider_run_attempt,
    trust_class            = COALESCE(NULLIF(EXCLUDED.trust_class, ''), github_provider_demands.trust_class),
    runner_class           = COALESCE(NULLIF(EXCLUDED.runner_class, ''), github_provider_demands.runner_class),
    workspace_scope_id     = COALESCE(EXCLUDED.workspace_scope_id, github_provider_demands.workspace_scope_id),
    last_delivery_id       = COALESCE(NULLIF(EXCLUDED.last_delivery_id, ''), github_provider_demands.last_delivery_id),
    updated_at             = now()
RETURNING state`

func (s *pgStore) EnsureProviderDemand(ctx context.Context, d demandRow) (string, error) {
	var state string
	err := s.pool.QueryRow(ctx, sqlEnsureProviderDemand,
		d.ProviderJobID, d.ProviderInstallationID, d.ProviderRepositoryID, d.RepositoryFullName,
		d.ProviderRunID, d.ProviderRunAttempt, d.TrustClass, d.RunnerClass,
		d.WorkspaceScopeID, d.LastDeliveryID,
	).Scan(&state)
	return state, err
}

// sqlAppendDemandProblem mirrors the delivery problem append, additionally
// guarded by demand existence so problems for deleted demands are silently
// skipped.
const sqlAppendDemandProblem = `
WITH demand AS (
    SELECT provider_job_id FROM github_provider_demands WHERE provider_job_id = $1
), next_seq AS (
    SELECT COALESCE(MAX(problem_seq), 0) + 1 AS seq
    FROM github_provider_demand_problems
    WHERE provider_job_id = $1
), ins AS (
    INSERT INTO github_provider_demand_problems (
        provider_job_id, problem_seq, phase, problem_type, problem_code,
        title, detail, docs_url, status, retryable, pointer, observed_at
    )
    SELECT demand.provider_job_id, next_seq.seq, $2, $3, $4, $5, $6, '', $7, $8, $9, now()
    FROM demand, next_seq
    RETURNING 1
)
UPDATE github_provider_demands d SET
    primary_problem_type   = CASE WHEN d.problem_count = 0 THEN $3 ELSE d.primary_problem_type END,
    primary_problem_code   = CASE WHEN d.problem_count = 0 THEN $4 ELSE d.primary_problem_code END,
    primary_problem_title  = CASE WHEN d.problem_count = 0 THEN $5 ELSE d.primary_problem_title END,
    primary_problem_detail = CASE WHEN d.problem_count = 0 THEN $6 ELSE d.primary_problem_detail END,
    primary_problem_status = CASE WHEN d.problem_count = 0 THEN $7 ELSE d.primary_problem_status END,
    problem_count          = d.problem_count + (SELECT COUNT(*) FROM ins),
    updated_at             = now()
WHERE d.provider_job_id = $1`

// sqlMarkProviderDemandFailed: terminal states are unrepeatable — the guard
// keeps assigned/completed demands from regressing (without it the sweeper
// hot-loops on terminal jobs).
const sqlMarkProviderDemandFailed = `
UPDATE github_provider_demands
SET state = 'capacity_failed', updated_at = now()
WHERE provider_job_id = $1 AND state NOT IN ('assigned', 'completed')`

func (s *pgStore) MarkProviderDemandFailed(ctx context.Context, providerJobID int64, problems []problem) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range problems {
		if _, err := tx.Exec(ctx, sqlAppendDemandProblem,
			providerJobID, phaseProcessing, p.typeURI(), p.Code, p.Title, p.Detail, p.Status, p.Retryable, p.Pointer); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, sqlMarkProviderDemandFailed, providerJobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type jobAssignment struct {
	ProviderJobID int64
	RunnerName    string
	RunnerID      int64
	DeliveryID    string
}

// A runner only ever runs one job: any stale claim under this runner name is
// evicted before the upsert. Written only from API reads.
const (
	sqlEvictStaleAssignment = `
DELETE FROM github_job_assignments WHERE runner_name = $2 AND provider_job_id <> $1`

	sqlUpsertJobAssignment = `
INSERT INTO github_job_assignments (provider_job_id, runner_name, runner_id, observed_from, delivery_id, observed_at)
VALUES ($1, $2, $3, 'github-api', $4, now())
ON CONFLICT (provider_job_id) DO UPDATE SET
    runner_name   = EXCLUDED.runner_name,
    runner_id     = EXCLUDED.runner_id,
    observed_from = EXCLUDED.observed_from,
    delivery_id   = EXCLUDED.delivery_id,
    observed_at   = EXCLUDED.observed_at,
    updated_at    = now()`

sqlAdoptAssignedExecution = `
UPDATE host_leases execution
SET state = 'ready', reported_state = 'assignment-routed',
    assignment_deadline_at = NULL, updated_at = now()
WHERE execution.provider_job_id = $1 AND execution.state = 'allocating'
  AND EXISTS (
      SELECT 1 FROM host_leases listener
      WHERE listener.lease_id = $2
        AND listener.host_id <> ''
        AND listener.state IN ('assigned', 'ready')
        AND listener.runner_class = execution.runner_class
  )
RETURNING host_id, runner_class`
)

func (s *pgStore) UpsertJobAssignment(ctx context.Context, a jobAssignment) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, sqlEvictStaleAssignment, a.ProviderJobID, a.RunnerName); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sqlUpsertJobAssignment, a.ProviderJobID, a.RunnerName, a.RunnerID, a.DeliveryID); err != nil {
		return err
	}
	var hostID, class string
	err = tx.QueryRow(ctx, sqlAdoptAssignedExecution,
		a.ProviderJobID, a.RunnerName).Scan(&hostID, &class)
	switch {
	case err == nil:
		// The job consumed an already-online listener before its own demand
		// could claim capacity. Any in-flight claim on its execution row is
		// returned; the selected listener's row already owns the real slot.
		if hostID != "" {
			if _, err := tx.Exec(ctx, sqlReleaseHostSlot, hostID, class); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, sqlAdvanceDemand, a.ProviderJobID, demandAssigned,
			[]string{demandRecorded, demandCapacityRequested}); err != nil {
			return err
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return err
	}
	return tx.Commit(ctx)
}

type queuedJob struct {
	ProviderJobID          int64
	ProviderInstallationID int64
	ProviderRunID          int64
	ProviderRunAttempt     int64
	ProviderRepositoryID   int64
	RepositoryFullName     string
	Name                   string
	RunnerClass            string
	Labels                 []string
}

// sqlListQueuedJobsForReconcile: the missed-webhook sweeper's candidate set —
// still-queued jobs of our class with no observed assignment and a demand
// that is absent or still just recorded. DISTINCT ON (repo, class) so one
// saturated repo/class cannot starve the batch. The updated_at floor is the
// stage-(a) API-read throttle (see reconcileQuietPeriod).
const sqlListQueuedJobsForReconcile = `
SELECT DISTINCT ON (j.provider_repository_id, j.runner_class)
    j.provider_job_id, j.provider_installation_id, j.provider_run_id, j.provider_run_attempt,
    j.provider_repository_id, j.repository_full_name, j.name,
    j.runner_class, j.labels_json::text
FROM github_workflow_jobs j
LEFT JOIN github_job_assignments a ON a.provider_job_id = j.provider_job_id
LEFT JOIN github_provider_demands d ON d.provider_job_id = j.provider_job_id
WHERE j.status = 'queued'
  AND j.runner_class <> ''
  AND a.provider_job_id IS NULL
  AND (d.provider_job_id IS NULL OR d.state = 'demand_recorded')
  AND j.updated_at <= now() - make_interval(secs => $2)
ORDER BY j.provider_repository_id, j.runner_class, j.provider_job_id
LIMIT $1`

func (s *pgStore) ListQueuedJobsForReconcile(ctx context.Context, batch int, quiet time.Duration) ([]queuedJob, error) {
	rows, err := s.pool.Query(ctx, sqlListQueuedJobsForReconcile, batch, quiet.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queuedJob
	for rows.Next() {
		var j queuedJob
		var labels string
		if err := rows.Scan(&j.ProviderJobID, &j.ProviderInstallationID, &j.ProviderRunID, &j.ProviderRunAttempt,
			&j.ProviderRepositoryID, &j.RepositoryFullName, &j.Name, &j.RunnerClass, &labels); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(labels), &j.Labels); err != nil {
			return nil, fmt.Errorf("labels_json for job %d: %w", j.ProviderJobID, err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// jobLockKeys derives the pg_try_advisory_lock (int4, int4) pair from the
// provider job id alone: big-endian split, each half masked positive — the
// same masking postflight used for its pair-key locks. This int4-pair keyspace
// is distinct from pg_advisory_xact_lock(int8) users (e.g. the migration
// lock), so they cannot collide.
func jobLockKeys(jobID int64) (int32, int32) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(jobID))
	hi := int32(binary.BigEndian.Uint32(b[0:4]) & 0x7FFFFFFF)
	lo := int32(binary.BigEndian.Uint32(b[4:8]) & 0x7FFFFFFF)
	return hi, lo
}

// TryJobLock takes the per-job advisory lock on a dedicated pooled conn (the
// unlock must run on the same session). Returns acquired=false without error
// when another worker holds the job.
func (s *pgStore) TryJobLock(ctx context.Context, jobID int64) (release func(), acquired bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	hi, lo := jobLockKeys(jobID)
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2)`, hi, lo).Scan(&got); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	release = func() {
		// Background context: the unlock must run even when the caller's
		// context is already cancelled, or the session leaks the lock.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1, $2)`, hi, lo)
		conn.Release()
	}
	return release, true, nil
}

type prCommentRow struct {
	ProviderRepositoryID   int64
	ProviderInstallationID int64
	RepositoryFullName     string
	PRNumber               int64
	ProviderCommentID      int64
	LastRenderedSHA256     string
	AttemptCount           int32
	// UpdatedAt as read by ListDirtyPRComments. The posted/clean statements
	// compare against it: a MarkPRCommentDirty landing mid-sync bumps
	// updated_at, so the clear leaves dirty=true and the newer state renders
	// on the next tick instead of being lost.
	UpdatedAt time.Time
}

const sqlMarkPRCommentDirty = `
INSERT INTO pr_comment_state (provider_repository_id, provider_installation_id, pr_number, repository_full_name, dirty)
VALUES ($1, $2, $3, $4, true)
ON CONFLICT (provider_repository_id, pr_number) DO UPDATE SET
    dirty = true,
    provider_installation_id = EXCLUDED.provider_installation_id,
    repository_full_name = EXCLUDED.repository_full_name,
    updated_at = now()`

func (s *pgStore) MarkPRCommentDirty(ctx context.Context, repositoryID, installationID int64, repositoryFullName string, prNumber int64) error {
	_, err := s.pool.Exec(ctx, sqlMarkPRCommentDirty, repositoryID, installationID, prNumber, repositoryFullName)
	return err
}

const sqlListDirtyPRComments = `
SELECT provider_repository_id, provider_installation_id, repository_full_name, pr_number,
    provider_comment_id, last_rendered_sha256, attempt_count, updated_at
FROM pr_comment_state
WHERE dirty AND (next_attempt_at IS NULL OR next_attempt_at <= now())
ORDER BY updated_at
LIMIT $1`

func (s *pgStore) ListDirtyPRComments(ctx context.Context, batch int) ([]prCommentRow, error) {
	rows, err := s.pool.Query(ctx, sqlListDirtyPRComments, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []prCommentRow
	for rows.Next() {
		var r prCommentRow
		if err := rows.Scan(&r.ProviderRepositoryID, &r.ProviderInstallationID, &r.RepositoryFullName, &r.PRNumber,
			&r.ProviderCommentID, &r.LastRenderedSHA256, &r.AttemptCount, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// The dirty flag is only cleared when updated_at still equals the value the
// sync started from ($n): a concurrent MarkPRCommentDirty bumps updated_at,
// so the SET leaves dirty=true and the newer job state is re-rendered next
// tick rather than silently dropped.
const sqlMarkPRCommentPosted = `
UPDATE pr_comment_state
SET provider_comment_id = $3, last_rendered_sha256 = $4,
    dirty = (updated_at <> $5),
    attempt_count = 0, next_attempt_at = NULL, updated_at = now()
WHERE provider_repository_id = $1 AND pr_number = $2`

func (s *pgStore) MarkPRCommentPosted(ctx context.Context, repositoryID, prNumber, commentID int64, renderedSHA256 string, listedUpdatedAt time.Time) error {
	_, err := s.pool.Exec(ctx, sqlMarkPRCommentPosted, repositoryID, prNumber, commentID, renderedSHA256, listedUpdatedAt)
	return err
}

const sqlMarkPRCommentClean = `
UPDATE pr_comment_state SET dirty = (updated_at <> $3), updated_at = now()
WHERE provider_repository_id = $1 AND pr_number = $2`

func (s *pgStore) MarkPRCommentClean(ctx context.Context, repositoryID, prNumber int64, listedUpdatedAt time.Time) error {
	_, err := s.pool.Exec(ctx, sqlMarkPRCommentClean, repositoryID, prNumber, listedUpdatedAt)
	return err
}

const sqlDeferPRComment = `
UPDATE pr_comment_state
SET attempt_count = attempt_count + 1, next_attempt_at = $3, updated_at = now()
WHERE provider_repository_id = $1 AND pr_number = $2`

func (s *pgStore) DeferPRComment(ctx context.Context, repositoryID, prNumber int64, nextAttempt time.Time) error {
	_, err := s.pool.Exec(ctx, sqlDeferPRComment, repositoryID, prNumber, nextAttempt)
	return err
}

// sqlListPRJobs: the comment's job set — our-class jobs on this PR, from the
// latest observed run attempt of each run (exact-attempt discipline: retried
// runs replace their earlier attempt's rows in the comment, they don't pile
// up next to them).
const sqlListPRJobs = `
SELECT j.workflow_name, j.name, j.runner_class, j.status, j.conclusion
FROM github_workflow_jobs j
WHERE j.provider_repository_id = $1
  AND j.pr_number = $2
  AND j.runner_class <> ''
  AND j.provider_run_attempt = (
      SELECT MAX(k.provider_run_attempt) FROM github_workflow_jobs k
      WHERE k.provider_run_id = j.provider_run_id
  )
ORDER BY j.workflow_name, j.name, j.provider_job_id`

func (s *pgStore) ListPRJobs(ctx context.Context, repositoryID, prNumber int64) ([]commentJob, error) {
	rows, err := s.pool.Query(ctx, sqlListPRJobs, repositoryID, prNumber)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []commentJob
	for rows.Next() {
		var j commentJob
		if err := rows.Scan(&j.WorkflowName, &j.Name, &j.RunnerClass, &j.Status, &j.Conclusion); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
