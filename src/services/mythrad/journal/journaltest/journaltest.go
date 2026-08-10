// Package journaltest is the conformance suite for journal.Journal
// implementations. The suite IS the interface contract: any backend that
// passes may serve as the durable truth of a park, and a backend swap is
// exactly "make this pass".
package journaltest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal"
)

// Run exercises the contract against a fresh implementation. factory must
// return a Journal over empty storage.
func Run(t *testing.T, factory func(t *testing.T) journal.Journal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Run("seqs_are_dense_from_one_and_read_ordered", func(t *testing.T) {
		j := factory(t)
		first, err := j.Append(ctx, 1, []journal.Event{ev(10, "a"), ev(10, "b")})
		if err != nil || first != 1 {
			t.Fatalf("first append: seq=%d err=%v, want 1, nil", first, err)
		}
		first, err = j.Append(ctx, 1, []journal.Event{ev(11, "c")})
		if err != nil || first != 3 {
			t.Fatalf("second append: seq=%d err=%v, want 3, nil", first, err)
		}
		var got []journal.Event
		if err := j.Read(ctx, 1, 1, func(e journal.Event) error {
			got = append(got, e)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("read %d events, want 3", len(got))
		}
		for i, e := range got {
			if e.Seq != int64(i+1) {
				t.Fatalf("event %d has seq %d: log not dense", i, e.Seq)
			}
		}
		if got[0].Actor != "a" || got[1].Actor != "b" || got[2].Actor != "c" {
			t.Fatalf("append order not preserved: %v", got)
		}
	})

	t.Run("read_from_seq_filters", func(t *testing.T) {
		j := factory(t)
		if _, err := j.Append(ctx, 1, []journal.Event{ev(1, "a"), ev(2, "b"), ev(3, "c")}); err != nil {
			t.Fatal(err)
		}
		var seqs []int64
		if err := j.Read(ctx, 1, 3, func(e journal.Event) error {
			seqs = append(seqs, e.Seq)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(seqs) != 1 || seqs[0] != 3 {
			t.Fatalf("Read(from=3) = %v, want [3]", seqs)
		}
	})

	t.Run("parks_are_isolated", func(t *testing.T) {
		j := factory(t)
		if first, err := j.Append(ctx, 7, []journal.Event{ev(1, "seven")}); err != nil || first != 1 {
			t.Fatalf("park 7: seq=%d err=%v", first, err)
		}
		if first, err := j.Append(ctx, 8, []journal.Event{ev(1, "eight")}); err != nil || first != 1 {
			t.Fatalf("park 8 must start at seq 1: seq=%d err=%v", first, err)
		}
		count := 0
		if err := j.Read(ctx, 8, 1, func(e journal.Event) error {
			count++
			if e.Actor != "eight" {
				return fmt.Errorf("park 8 sees park 7 event %q", e.Actor)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("park 8 has %d events, want 1", count)
		}
	})

	t.Run("payloads_and_u64_fields_round_trip_exactly", func(t *testing.T) {
		j := factory(t)
		payload := []byte{0, 0xFF, 0xD7, 0, 1, 2, 0}
		in := journal.Event{
			Tick:     1<<63 + 5,
			Epoch:    3,
			Kind:     0xBEEF,
			Actor:    "sub|weird:chars",
			IntentID: 1<<64 - 2,
			Payload:  payload,
		}
		if _, err := j.Append(ctx, 1, []journal.Event{in}); err != nil {
			t.Fatal(err)
		}
		if err := j.Read(ctx, 1, 1, func(e journal.Event) error {
			if e.Tick != in.Tick || e.Epoch != in.Epoch || e.Kind != in.Kind ||
				e.Actor != in.Actor || e.IntentID != in.IntentID || !bytes.Equal(e.Payload, in.Payload) {
				return fmt.Errorf("round-trip mangled: %+v != %+v", e, in)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("concurrent_writers_never_interleave_or_gap", func(t *testing.T) {
		j := factory(t)
		const perWriter = 20
		var wg sync.WaitGroup
		for w := 0; w < 2; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < perWriter; i++ {
					for {
						_, err := j.Append(ctx, 1, []journal.Event{ev(uint64(i), fmt.Sprintf("w%d", w))})
						if err == nil {
							break
						}
						if !errors.Is(err, journal.ErrConflict) {
							t.Errorf("writer %d: %v", w, err)
							return
						}
						// A real authority stops on ErrConflict; the retry here
						// exists to prove the log stays dense under the race.
					}
				}
			}(w)
		}
		wg.Wait()
		var last int64
		if err := j.Read(ctx, 1, 1, func(e journal.Event) error {
			if e.Seq != last+1 {
				return fmt.Errorf("gap: %d then %d", last, e.Seq)
			}
			last = e.Seq
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if last != 2*perWriter {
			t.Fatalf("log has %d events, want %d", last, 2*perWriter)
		}
	})

	t.Run("snapshots_latest_wins_and_round_trips", func(t *testing.T) {
		j := factory(t)
		if _, ok, err := j.LatestSnapshot(ctx, 1); err != nil || ok {
			t.Fatalf("empty park: ok=%v err=%v, want false, nil", ok, err)
		}
		state := []byte{0x4D, 0x59, 0x50, 0x31, 0, 1, 2, 0xFF}
		older := journal.Snapshot{Seq: 10, Tick: 240, Epoch: 1, WH: 1<<63 + 7, State: state}
		newer := journal.Snapshot{Seq: 20, Tick: 480, Epoch: 2, WH: 42, State: append(state, 9)}
		if err := j.PutSnapshot(ctx, 1, older); err != nil {
			t.Fatal(err)
		}
		if err := j.PutSnapshot(ctx, 1, newer); err != nil {
			t.Fatal(err)
		}
		got, ok, err := j.LatestSnapshot(ctx, 1)
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if got.Seq != newer.Seq || got.Tick != newer.Tick || got.Epoch != newer.Epoch ||
			got.WH != newer.WH || !bytes.Equal(got.State, newer.State) {
			t.Fatalf("latest = %+v, want %+v", got, newer)
		}
	})

	t.Run("empty_append_is_an_error", func(t *testing.T) {
		j := factory(t)
		if _, err := j.Append(ctx, 1, nil); err == nil {
			t.Fatal("empty append must error, not mint a seq")
		}
	})
}

func ev(tick uint64, actor string) journal.Event {
	return journal.Event{Tick: tick, Epoch: 1, Kind: 1, Actor: actor, Payload: []byte{1}}
}
