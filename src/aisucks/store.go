package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/aisucks/db"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrateLockKey serializes Migrate across replicas. The value is arbitrary
// but must never change: two binaries disagreeing on the key would migrate
// concurrently.
const migrateLockKey = 0x61697375636b73 // "aisucks"

// Report is one accepted submission: the canonical share URL plus the
// extracted transcript. Nothing about the submitter exists to store.
type Report struct {
	ShareURL      string
	Provider      string
	Model         string
	ParserVersion int
	Status        string // "stored" | "parse_failed"
	Turns         []Turn
}

type Store struct {
	pool *pgxpool.Pool
}

// openStore polls until Postgres accepts connections: the database converges
// in parallel with this process and may answer seconds to minutes later.
func openStore(ctx context.Context, dsn string, patience time.Duration) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	deadline := time.Now().Add(patience)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			// Pool collectors register here, not at process start: they
			// close over the pool, which exists only once the store does.
			registerPoolMetrics(pool)
			return &Store{pool: pool}, nil
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("store: database did not answer within %s: %w", patience, err)
		}
		time.Sleep(3 * time.Second)
	}
}

func (s *Store) Close() { s.pool.Close() }

// Migrate applies embedded migrations in filename order under an advisory
// lock. Migrations are additive-only by policy (docs/runbooks): the previous
// binary must always run against the current schema, or rollback dies.
func (s *Store) Migrate(ctx context.Context) error {
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name())
	}
	sort.Strings(names)

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockKey)

	if _, err := conn.Exec(ctx,
		"CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for _, name := range names {
		var done bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)", name).Scan(&done); err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		if done {
			continue
		}
		sql, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name) VALUES ($1)", name); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		fmt.Fprintf(os.Stderr, "migrated %s\n", name)
	}
	return nil
}

// Insert stores a report and its turns atomically, and is idempotent: a
// share URL already present is a silent no-op, NOT a distinguishable error.
// This is a privacy guarantee, not a convenience — surfacing "already
// reported" would let anyone holding a share link probe whether it is in
// the corpus (and the labs hold every one of their links), leaking corpus
// membership and submitter behavior. Caller and submitter learn the same
// thing whether the link was new or a repeat: nothing (charter value 2).
// The created flag (false on the no-op) exists solely for the aggregate
// metrics counter on the loopback diagnostics endpoint; it carries no link
// identity and must never reach a response.
func (s *Store) Insert(ctx context.Context, r Report) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("insert: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	q := db.New(tx)
	id, err := q.InsertReport(ctx, db.InsertReportParams{
		ShareUrl:      r.ShareURL,
		Provider:      r.Provider,
		Model:         r.Model,
		ParserVersion: int32(r.ParserVersion),
		Status:        r.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // already present; idempotent no-op (see doc comment)
	}
	if err != nil {
		return false, fmt.Errorf("insert: %w", err)
	}
	if len(r.Turns) > 0 {
		rows := make([]db.InsertTurnsParams, len(r.Turns))
		for i, t := range r.Turns {
			rows[i] = db.InsertTurnsParams{ReportID: id, Idx: int32(i), Role: t.Role, Content: t.Content}
		}
		if _, err := q.InsertTurns(ctx, rows); err != nil {
			return false, fmt.Errorf("insert turns: %w", err)
		}
	}
	return true, tx.Commit(ctx)
}

// Healthy is the healthz probe: can we reach the database right now.
func (s *Store) Healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.pool.Ping(ctx)
}
