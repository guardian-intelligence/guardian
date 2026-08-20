package chunkie

// The shadow window's core claim, in miniature: the ticklog mirrors the
// Postgres journal event-for-event — same seqs, same ticks, same kinds,
// same payload bytes, actors resolved identically. The prod comparator
// (WAL replay vs `aspect mythra dump`) is this test writ large.

import (
	"time"
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/ticklog"
	"github.com/guardian-intelligence/guardian/src/chunkies/journal"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/postflight/controlplane/pgtest"
)

func TestShadowWALMirrorsJournal(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	j := journal.NewPg(pool)
	if err := j.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	module := toyModule(t)
	dir := t.TempDir()

	// An hour past the epoch, so the attach tip is a real tick number —
	// the frontier below is exclusive and cannot express "before tick 0".
	clock := fixedClock(wallEpoch.Add(time.Hour))
	a, err := openAuthority(ctx, "chunk-shadow", module, nil, toyVocab(), j, toyMods(module), clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The same attach the registry performs: at the exact journal tip,
	// before any tick runs. Open itself may have minted seqs (genesis
	// system events, the clock repayment); the shadow covers strictly
	// newer history, and its first record may land AT the attach tick —
	// so the replay key's exclusive frontier is StartTick-1.
	openSeq, openTick := a.lastSeq, a.host.Tick()
	a.wal = newShadowFactory(dir)("chunk-shadow", a.host.Epoch(), openTick, openSeq)
	if a.wal == nil {
		t.Fatal("shadow WAL failed to open")
	}

	s := &session{sub: "alice", actorID: codec.ActorFor("alice"), out: make(chan []byte, 16)}
	a.stageIntent(s, 1, kJoin, nil)
	a.tickOnce()
	a.stageIntent(s, 2, kMove, move(5))
	a.stageIntent(s, 3, kMove, move(-3))
	for i := 0; i < 50; i++ {
		a.tickOnce()
	}
	// A live rate boundary rides the batch lane as a system event.
	done := make(chan error, 1)
	if err := a.stageRateChange(rateChangeReq{hz: 48, done: done}); err != nil {
		t.Fatal(err)
	}
	a.tickOnce()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	a.stageIntent(s, 4, kMove, move(2))
	a.tickOnce()
	lastSeq := a.lastSeq
	if lastSeq < 5 {
		t.Fatalf("journal lastSeq = %d, want >= 5", lastSeq)
	}
	a.close() // barriers and closes the shadow, releases the writer lock
	a.host.close()

	// Replay the shadow and index it by seq.
	type walEvent struct {
		tick uint64
		rec  codec.EventRecord
	}
	walBySeq := map[int64]walEvent{}
	tips, err := ticklog.Scan(dir, []ticklog.ChunkKey{{Name: "chunk-shadow", Lineage: 0, AfterTick: openTick - 1, AfterSeq: openSeq}}, func(chunk string, r codec.Record) error {
		recs, err := codec.ParseRecords(r.Records, int(r.Count))
		if err != nil {
			return err
		}
		for i, rec := range recs {
			// Scan reuses its frame buffer between records: anything the
			// callback retains must be copied.
			rec.Payload = append([]byte(nil), rec.Payload...)
			walBySeq[r.FirstSeq+int64(i)] = walEvent{tick: r.Tick, rec: rec}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if tip := tips["chunk-shadow"]; tip.Seq != lastSeq {
		t.Fatalf("wal tip seq = %d, journal lastSeq = %d", tip.Seq, lastSeq)
	}

	// Diff against the journal row-for-row, from the attach point on.
	rows := 0
	err = j.Read(ctx, a.id, openSeq+1, func(ev journal.Event) error {
		rows++
		w, ok := walBySeq[ev.Seq]
		if !ok {
			t.Fatalf("journal seq %d missing from the wal", ev.Seq)
		}
		wantActor, wantPayload := a.vocab.eventActor(ev.Kind, ev.Actor, ev.Payload)
		if w.tick != ev.Tick || w.rec.Kind != ev.Kind || w.rec.Intent != ev.IntentID ||
			w.rec.Actor != wantActor || string(w.rec.Payload) != string(wantPayload) {
			t.Fatalf("seq %d diverges: wal={tick %d kind %d intent %d actor %d payload %x} journal={tick %d kind %d intent %d actor %d payload %x}",
				ev.Seq, w.tick, w.rec.Kind, w.rec.Intent, w.rec.Actor, w.rec.Payload,
				ev.Tick, ev.Kind, ev.IntentID, wantActor, wantPayload)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if int64(rows) != lastSeq-openSeq || len(walBySeq) != rows {
		t.Fatalf("row counts diverge: journal %d, wal %d, lastSeq %d (open %d)", rows, len(walBySeq), lastSeq, openSeq)
	}
}
