package main

import (
	"context"
	"fmt"
	"strings"
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

// generationRow carries the catalog columns that exist today plus the
// per-generation size statistics as optional pointers: the hammer reads
// whatever the deployed schema offers and reports what it finds.
type generationRow struct {
	Generation    string     `json:"generation"`
	HostID        string     `json:"host_id"`
	RunnerClass   string     `json:"runner_class"`
	State         string     `json:"state"`
	Bytes         int64      `json:"bytes"`
	Pinned        bool       `json:"pinned"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ScopeID       *string    `json:"scope_id,omitempty"`
	Used          *int64     `json:"used,omitempty"`
	LogicalUsed   *int64     `json:"logicalused,omitempty"`
	Written       *int64     `json:"written,omitempty"`
	CompressRatio *float64   `json:"compressratio,omitempty"`
	SealedAt      *time.Time `json:"sealed_at,omitempty"`
}

type scopeRow struct {
	ScopeID             string  `json:"scope_id"`
	CurrentGenerationID *string `json:"current_generation_id,omitempty"`
	HomeHostID          *string `json:"home_host_id,omitempty"`
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

const sqlGenerationColumns = `
SELECT column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'workspace_generations'`

// Generations reads the full catalog. The size-statistics and scope columns
// are introspected: they land with the generation-lifecycle migration and the
// hammer must degrade to the columns actually deployed.
func (d *dbClient) Generations(ctx context.Context) ([]generationRow, error) {
	present := map[string]bool{}
	rows, err := d.pool.Query(ctx, sqlGenerationColumns)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		present[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cols := []string{"generation", "host_id", "runner_class", "state", "bytes", "pinned", "created_at", "updated_at"}
	optional := []string{"scope_id", "used", "logicalused", "written", "compressratio", "sealed_at"}
	var picked []string
	for _, c := range optional {
		if present[c] {
			picked = append(picked, c)
		}
	}
	query := fmt.Sprintf("SELECT %s FROM workspace_generations ORDER BY created_at, generation",
		strings.Join(append(append([]string{}, cols...), picked...), ", "))
	rows, err = d.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []generationRow
	for rows.Next() {
		var g generationRow
		dest := []any{&g.Generation, &g.HostID, &g.RunnerClass, &g.State, &g.Bytes, &g.Pinned, &g.CreatedAt, &g.UpdatedAt}
		for _, c := range picked {
			switch c {
			case "scope_id":
				dest = append(dest, &g.ScopeID)
			case "used":
				dest = append(dest, &g.Used)
			case "logicalused":
				dest = append(dest, &g.LogicalUsed)
			case "written":
				dest = append(dest, &g.Written)
			case "compressratio":
				dest = append(dest, &g.CompressRatio)
			case "sealed_at":
				dest = append(dest, &g.SealedAt)
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const sqlScopeColumns = `
SELECT column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'workspace_scopes'`

// Scopes reads the scope pointer table when it exists; known=false means the
// deployed schema predates it and scope invariants cannot be checked.
func (d *dbClient) Scopes(ctx context.Context) ([]scopeRow, bool, error) {
	present := map[string]bool{}
	rows, err := d.pool.Query(ctx, sqlScopeColumns)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, false, err
		}
		present[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(present) == 0 || !present["current_generation_id"] {
		return nil, false, nil
	}
	idCol := ""
	for _, candidate := range []string{"scope_id", "scope_key", "id"} {
		if present[candidate] {
			idCol = candidate
			break
		}
	}
	if idCol == "" {
		return nil, false, nil
	}
	homeCol := "NULL"
	if present["home_host_id"] {
		homeCol = "home_host_id"
	}
	query := fmt.Sprintf("SELECT %s::text, current_generation_id, %s FROM workspace_scopes ORDER BY 1", idCol, homeCol)
	rows, err = d.pool.Query(ctx, query)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []scopeRow
	for rows.Next() {
		var s scopeRow
		if err := rows.Scan(&s.ScopeID, &s.CurrentGenerationID, &s.HomeHostID); err != nil {
			return nil, false, err
		}
		out = append(out, s)
	}
	return out, true, rows.Err()
}

func (d *dbClient) Snapshot(ctx context.Context) (*dbSnapshot, error) {
	slots, err := d.SnapshotSlots(ctx)
	if err != nil {
		return nil, err
	}
	generations, err := d.Generations(ctx)
	if err != nil {
		return nil, err
	}
	scopes, known, err := d.Scopes(ctx)
	if err != nil {
		return nil, err
	}
	return &dbSnapshot{
		CapturedAt:  time.Now().UTC(),
		Slots:       slots,
		Generations: generations,
		Scopes:      scopes,
		ScopesKnown: known,
	}, nil
}
