// Package journal is the durable truth of every dog chunk: an ordered,
// per-chunk event log plus periodic state snapshots (src/chunkies/README.md).
// Restore, replay, rejoin, spectating, and time-travel debugging all read
// the same log this package writes.
//
// The contract lives in this interface, not in Postgres. Any backend must
// pass journaltest.Run: per-chunk seqs are dense, gap-free, monotonic, and
// assigned at append; a split-brain second writer conflicts instead of
// interleaving; payloads and snapshot state are opaque bytes end to end.
package journal

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is one journal entry. Seq is assigned by Append. Tick, IntentID,
// and the world-hash in Snapshot are uint64 in the sim ABI and stored as
// their two's-complement bigint image; the round-trip is exact.
type Event struct {
	Seq      int64
	Tick     uint64
	Epoch    uint32
	Kind     uint16
	Actor    string
	IntentID uint64
	Payload  []byte
}

// Snapshot is a canonical sim state blob at a journal position: replaying
// events with Seq > s.Seq on top of State must reproduce the live chunk.
// TerrainID names the terrain artifact the state was captured on; the sim
// refuses to restore against any other, so the host must load that blob
// first.
type Snapshot struct {
	Seq       int64
	Tick      uint64
	Epoch     uint32
	WH        uint64
	TerrainID uint64
	State     []byte
}

// ErrConflict reports a lost single-writer race: the log has moved past
// afterSeq, so another writer exists (or this caller is stale). The caller
// is not the chunk's authority anymore; it must not retry blindly.
var ErrConflict = errors.New("journal: append conflict (second writer?)")

type Journal interface {
	// Append atomically appends events at afterSeq+1..afterSeq+len,
	// returning the first assigned seq. afterSeq is the caller's cached
	// log position — the single-writer enforcement: if the log has moved,
	// the append fails with ErrConflict instead of interleaving. Either
	// every event lands or none do.
	Append(ctx context.Context, chunkID int64, afterSeq int64, events []Event) (int64, error)
	// Read streams events with Seq >= fromSeq in seq order.
	Read(ctx context.Context, chunkID int64, fromSeq int64, fn func(Event) error) error
	PutSnapshot(ctx context.Context, chunkID int64, s Snapshot) error
	// LatestSnapshot returns the highest-seq snapshot, if any exists.
	LatestSnapshot(ctx context.Context, chunkID int64) (Snapshot, bool, error)
	// PutTerrain stores a terrain artifact under its content identity.
	// Blobs are immutable per id: re-putting an existing id is a no-op, so
	// ten thousand chunks born from one fixture share one row.
	PutTerrain(ctx context.Context, terrainID uint64, schema uint32, blob []byte) error
	// TerrainBlob fetches a terrain artifact by content identity.
	TerrainBlob(ctx context.Context, terrainID uint64) ([]byte, bool, error)
}

//go:embed migrations/*.sql
var migrationFS embed.FS

// Pg is the Postgres-backed Journal.
type Pg struct {
	pool *pgxpool.Pool
}

func NewPg(pool *pgxpool.Pool) *Pg { return &Pg{pool: pool} }

// Migrate applies the schema, recorded per service in
// guardian_schema_migrations (same ledger the other product services use).
func (p *Pg) Migrate(ctx context.Context) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS guardian_schema_migrations (
			service text NOT NULL,
			version text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (service, version)
		)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM guardian_schema_migrations
				WHERE service = 'mythra' AND version = $1
			)`, entry.Name(),
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if exists {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO guardian_schema_migrations (service, version)
			 VALUES ('mythra', $1)`, entry.Name(),
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (p *Pg) Append(ctx context.Context, chunkID int64, afterSeq int64, events []Event) (int64, error) {
	if len(events) == 0 {
		return 0, errors.New("journal: empty append")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	batch := &pgx.Batch{}
	for i, ev := range events {
		batch.Queue(
			`INSERT INTO park_events
			   (park_id, seq, tick, epoch, kind, actor, intent_id, payload)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			chunkID, afterSeq+1+int64(i), int64(ev.Tick), int32(ev.Epoch),
			int16(ev.Kind), ev.Actor, int64(ev.IntentID), ev.Payload)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		// The (park_id, seq) primary key is the single-writer enforcement:
		// a log that moved past afterSeq collides here and the stale
		// writer loses instead of interleaving.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, ErrConflict
		}
		return 0, err
	}
	// A gap (afterSeq ahead of the log) must also fail: seqs are dense.
	var prior bool
	if err := tx.QueryRow(ctx,
		`SELECT $2 = 0 OR EXISTS (
		   SELECT 1 FROM park_events WHERE park_id = $1 AND seq = $2
		 )`, chunkID, afterSeq,
	).Scan(&prior); err != nil {
		return 0, err
	}
	if !prior {
		return 0, ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return afterSeq + 1, nil
}

func (p *Pg) Read(ctx context.Context, chunkID int64, fromSeq int64, fn func(Event) error) error {
	rows, err := p.pool.Query(ctx,
		`SELECT seq, tick, epoch, kind, actor, intent_id, payload
		   FROM park_events
		  WHERE park_id = $1 AND seq >= $2
		  ORDER BY seq`,
		chunkID, fromSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ev Event
		var tick, intentID int64
		var epoch int32
		var kind int16
		if err := rows.Scan(&ev.Seq, &tick, &epoch, &kind, &ev.Actor, &intentID, &ev.Payload); err != nil {
			return err
		}
		ev.Tick, ev.Epoch, ev.Kind, ev.IntentID = uint64(tick), uint32(epoch), uint16(kind), uint64(intentID)
		if err := fn(ev); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *Pg) PutSnapshot(ctx context.Context, chunkID int64, s Snapshot) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO park_snapshots (park_id, seq, tick, epoch, wh, terrain_id, state)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (park_id, seq) DO NOTHING`,
		chunkID, s.Seq, int64(s.Tick), int32(s.Epoch), int64(s.WH), int64(s.TerrainID), s.State)
	return err
}

func (p *Pg) LatestSnapshot(ctx context.Context, chunkID int64) (Snapshot, bool, error) {
	var s Snapshot
	var tick, wh, terrainID int64
	var epoch int32
	err := p.pool.QueryRow(ctx,
		`SELECT seq, tick, epoch, wh, terrain_id, state
		   FROM park_snapshots
		  WHERE park_id = $1
		  ORDER BY seq DESC
		  LIMIT 1`,
		chunkID,
	).Scan(&s.Seq, &tick, &epoch, &wh, &terrainID, &s.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	s.Tick, s.Epoch, s.WH, s.TerrainID = uint64(tick), uint32(epoch), uint64(wh), uint64(terrainID)
	return s, true, nil
}

func (p *Pg) PutTerrain(ctx context.Context, terrainID uint64, schema uint32, blob []byte) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO park_terrain (terrain_id, schema, blob)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (terrain_id) DO NOTHING`,
		int64(terrainID), int32(schema), blob)
	return err
}

func (p *Pg) TerrainBlob(ctx context.Context, terrainID uint64) ([]byte, bool, error) {
	var blob []byte
	err := p.pool.QueryRow(ctx,
		`SELECT blob FROM park_terrain WHERE terrain_id = $1`,
		int64(terrainID),
	).Scan(&blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return blob, true, nil
}
