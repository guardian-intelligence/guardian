package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Control-plane vocabulary the hammer joins against. Kept as data here (not
// imported) because the hammer must keep working while the control plane's
// vocabulary grows: an unknown state is simply non-terminal.
var (
	terminalDemandStates = map[string]bool{
		"completed":       true,
		"capacity_failed": true,
		"jit_failed":      true,
		"sandbox_failed":  true,
	}
	terminalLeaseStates = map[string]bool{
		"completed": true,
		"failed":    true,
		"expired":   true,
	}
)

type slotRow struct {
	HostID   string `json:"host_id"`
	Class    string `json:"class"`
	Total    int    `json:"total"`
	Warm     int    `json:"warm"`
	Used     int    `json:"used"`
	Reserved int    `json:"reserved"`
}

type demandRow struct {
	ProviderJobID int64     `json:"provider_job_id"`
	ProviderRunID int64     `json:"provider_run_id"`
	RunAttempt    int64     `json:"run_attempt"`
	Repo          string    `json:"repo"`
	RunnerClass   string    `json:"runner_class"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type leaseRow struct {
	LeaseID             string    `json:"lease_id"`
	ProviderJobID       int64     `json:"provider_job_id"`
	State               string    `json:"state"`
	ReportedState       string    `json:"reported_state"`
	HostID              string    `json:"host_id"`
	RunnerClass         string    `json:"runner_class"`
	WorkspaceGeneration string    `json:"workspace_generation"`
	SealGeneration      string    `json:"seal_generation"`
	ExitCode            *int      `json:"exit_code,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type scopeRow struct {
	ScopeID           string `json:"scope_id"`
	Org               string `json:"org"`
	Repo              string `json:"repo"`
	ScopeRef          string `json:"scope_ref"`
	WorkflowPath      string `json:"workflow_path"`
	JobName           string `json:"job_name"`
	MatrixKey         string `json:"matrix_key"`
	RunnerClass       string `json:"runner_class"`
	CurrentGeneration string `json:"current_generation"`
	HomeHostID        string `json:"home_host_id"`
}

type generationRow struct {
	Generation  string    `json:"generation"`
	HostID      string    `json:"host_id"`
	RunnerClass string    `json:"runner_class"`
	State       string    `json:"state"`
	Bytes       int64     `json:"bytes"`
	Pinned      bool      `json:"pinned"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type dbClient struct {
	pool *pgxpool.Pool
}

func openDB(ctx context.Context, dsn string) (*dbClient, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	return &dbClient{pool: pool}, nil
}

func (d *dbClient) Close() { d.pool.Close() }

const sqlSnapshotSlots = `
SELECT host_id, class, total, warm, used, reserved
FROM host_slots ORDER BY host_id, class`

func (d *dbClient) SnapshotSlots(ctx context.Context) ([]slotRow, error) {
	rows, err := d.pool.Query(ctx, sqlSnapshotSlots)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []slotRow
	for rows.Next() {
		var s slotRow
		if err := rows.Scan(&s.HostID, &s.Class, &s.Total, &s.Warm, &s.Used, &s.Reserved); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const sqlDemandsSince = `
SELECT provider_job_id, provider_run_id, provider_run_attempt,
    repository_full_name, runner_class, state, created_at, updated_at
FROM github_provider_demands
WHERE created_at >= $1
ORDER BY created_at`

func (d *dbClient) DemandsSince(ctx context.Context, since time.Time) ([]demandRow, error) {
	rows, err := d.pool.Query(ctx, sqlDemandsSince, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []demandRow
	for rows.Next() {
		var r demandRow
		if err := rows.Scan(&r.ProviderJobID, &r.ProviderRunID, &r.RunAttempt,
			&r.Repo, &r.RunnerClass, &r.State, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const sqlLeasesSince = `
SELECT lease_id, provider_job_id, state, reported_state, host_id, runner_class,
    workspace_generation, seal_generation, exit_code, created_at, updated_at
FROM host_leases
WHERE created_at >= $1
ORDER BY created_at`

func (d *dbClient) LeasesSince(ctx context.Context, since time.Time) ([]leaseRow, error) {
	rows, err := d.pool.Query(ctx, sqlLeasesSince, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []leaseRow
	for rows.Next() {
		var r leaseRow
		if err := rows.Scan(&r.LeaseID, &r.ProviderJobID, &r.State, &r.ReportedState, &r.HostID,
			&r.RunnerClass, &r.WorkspaceGeneration, &r.SealGeneration, &r.ExitCode,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const sqlDeliveriesSince = `
SELECT provider_job_id, min(received_at)
FROM github_webhook_deliveries
WHERE event_name = 'workflow_job' AND provider_job_id <> 0 AND received_at >= $1
GROUP BY provider_job_id`

// DeliveriesSince returns each job's earliest workflow_job webhook arrival —
// the authoritative start of the pickup measurement.
func (d *dbClient) DeliveriesSince(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	rows, err := d.pool.Query(ctx, sqlDeliveriesSince, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var jobID int64
		var at time.Time
		if err := rows.Scan(&jobID, &at); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d", jobID)] = at
	}
	return out, rows.Err()
}

const sqlScopes = `
SELECT scope_id, org, repo, scope_ref, workflow_path, job_name, matrix_key,
    runner_class, COALESCE(current_generation_id, ''), home_host_id
FROM workspace_scopes ORDER BY org, repo, workflow_path, job_name, matrix_key, runner_class`

func (d *dbClient) Scopes(ctx context.Context) ([]scopeRow, error) {
	rows, err := d.pool.Query(ctx, sqlScopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scopeRow
	for rows.Next() {
		var s scopeRow
		if err := rows.Scan(&s.ScopeID, &s.Org, &s.Repo, &s.ScopeRef, &s.WorkflowPath,
			&s.JobName, &s.MatrixKey, &s.RunnerClass, &s.CurrentGeneration, &s.HomeHostID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const sqlGenerations = `
SELECT generation, host_id, runner_class, state, bytes, pinned, created_at, updated_at
FROM workspace_generations ORDER BY created_at, generation`

func (d *dbClient) Generations(ctx context.Context) ([]generationRow, error) {
	rows, err := d.pool.Query(ctx, sqlGenerations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []generationRow
	for rows.Next() {
		var g generationRow
		if err := rows.Scan(&g.Generation, &g.HostID, &g.RunnerClass, &g.State, &g.Bytes,
			&g.Pinned, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (d *dbClient) Snapshot(ctx context.Context) (*dbSnapshot, error) {
	slots, err := d.SnapshotSlots(ctx)
	if err != nil {
		return nil, err
	}
	scopes, err := d.Scopes(ctx)
	if err != nil {
		return nil, err
	}
	generations, err := d.Generations(ctx)
	if err != nil {
		return nil, err
	}
	return &dbSnapshot{
		CapturedAt:  time.Now().UTC(),
		Slots:       slots,
		Scopes:      scopes,
		Generations: generations,
	}, nil
}
