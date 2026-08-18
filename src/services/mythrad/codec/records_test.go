package codec

import (
	"bytes"
	"testing"
)

// A segment is replayable from any clean state, and every way a crash can
// shear its tail must read as torn, never as corrupt or as success.
func TestSegmentReplayAndTornTails(t *testing.T) {
	seg := EncodeSegmentHeader(SegmentHeader{Version: 1, Lineage: 7, Generation: 3, FirstTick: 100})
	body := AppendTickRecord(nil, 100, 0, 1, 2, fxRun(), fxWH)
	body = AppendWatermark(body, 180)
	body = AppendTickRecord(body, 200, 2, 1, 1, fxRec1(), fxWH+1)

	if _, err := DecodeSegmentHeader(seg); err != nil {
		t.Fatalf("segment header: %v", err)
	}

	// Full replay: three records then a clean end.
	var got []Record
	rest := body
	for {
		rec, n, err := ReadRecord(rest)
		if err == ErrEnd {
			break
		}
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		got = append(got, rec)
		rest = rest[n:]
	}
	if len(got) != 3 || got[0].Type != RecordTick || got[1].Type != RecordWatermark || got[2].Type != RecordTick {
		t.Fatalf("replay saw %+v", got)
	}
	if got[0].Count != 2 || !bytes.Equal(got[0].Records, fxRun()) || got[2].WH != fxWH+1 {
		t.Fatalf("replayed records disagree with what was appended")
	}
	if got[1].Tick != 180 {
		t.Fatalf("watermark tick = %d", got[1].Tick)
	}

	// Every truncation point inside the final record is a torn tail. The
	// boundary before it replays the first two records then reports End.
	lastStart := len(body) - (len(AppendTickRecord(nil, 200, 2, 1, 1, fxRec1(), fxWH+1)))
	for cut := lastStart + 1; cut < len(body); cut++ {
		rest := body[:cut]
		var err error
		var n int
		for {
			_, n, err = ReadRecord(rest)
			if err != nil {
				break
			}
			rest = rest[n:]
		}
		if err != ErrTornTail {
			t.Fatalf("cut at %d: err = %v, want torn tail", cut, err)
		}
	}

	// Preallocated zero-fill after real records reads as torn, not corrupt.
	padded := append(append([]byte{}, body...), make([]byte, 64)...)
	rest = padded
	var err error
	var n int
	for {
		_, n, err = ReadRecord(rest)
		if err != nil {
			break
		}
		rest = rest[n:]
	}
	if err != ErrTornTail {
		t.Fatalf("zero-fill tail: err = %v, want torn tail", err)
	}

	// A flipped byte mid-segment is corruption, not a tail.
	bad := append([]byte{}, body...)
	bad[9] ^= 0xFF
	if _, _, err := ReadRecord(bad); err != ErrCorrupt {
		t.Fatalf("flipped byte: err = %v, want corrupt", err)
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	want := fxCheckpoint()
	got, err := DecodeCheckpoint(EncodeCheckpoint(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Game != want.Game || got.Chunk != want.Chunk || got.Lineage != want.Lineage ||
		got.Generation != want.Generation || got.Seq != want.Seq || got.Tick != want.Tick ||
		got.Epoch != want.Epoch || got.WH != want.WH || got.Content != want.Content ||
		got.CW != want.CW || got.PW != want.PW ||
		len(got.Dedup) != len(want.Dedup) || got.Dedup[0] != want.Dedup[0] ||
		!bytes.Equal(got.State, want.State) {
		t.Fatalf("round trip disagrees:\n got %+v\nwant %+v", got, want)
	}
	// Truncation anywhere fails closed.
	full := EncodeCheckpoint(want)
	for _, cut := range []int{4, 8, len(full) / 2, len(full) - 1} {
		if _, err := DecodeCheckpoint(full[:cut]); err == nil {
			t.Fatalf("truncated checkpoint at %d decoded", cut)
		}
	}
}

// The record run built from verbatim intent bytes must fan out and land on
// disk unchanged — the zero-rebuild property, checked at the API level.
func TestVerbatimRunSharedByTickAndWAL(t *testing.T) {
	// A "received" intent frame, as the relay would hold it.
	frame := EncodeIntent(fxIntent, fxKind, fxActor, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	r := NewReader(bytes.NewReader(frame))
	_, payload, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := DecodeIntent(payload)
	if err != nil {
		t.Fatal(err)
	}

	// The authority validates via the SimEvent slice, then appends the
	// arrived bytes — not a re-encoding — to the tick's run.
	run := append([]byte{}, payload...)
	run = AppendEventRecord(run, SystemIntent, fxSysKind, 0, []byte{0x0A, 0x0B, 0x0C, 0x0D})

	wire := EncodeTick(fxTick, fxSeq, 2, run)
	wal := AppendTickRecord(nil, fxTick, fxSeq, fxEpoch, 2, run, fxWH)
	if !bytes.Contains(wire, payload) || !bytes.Contains(wal, payload) {
		t.Fatal("intent bytes were not carried verbatim")
	}
	if len(rec.SimEvent) != SimEventHeader+4 || rec.Actor != fxActor {
		t.Fatalf("SimEvent slice misparsed: %+v", rec)
	}

	// And the client's decode of the fan-out sees the same records.
	fr := NewReader(bytes.NewReader(wire))
	_, p, err := fr.Next()
	if err != nil {
		t.Fatal(err)
	}
	_, recs, err := DecodeTick(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Intent != fxIntent || recs[1].Intent != SystemIntent ||
		!bytes.Equal(recs[0].SimEvent, rec.SimEvent) {
		t.Fatalf("fan-out decode disagrees: %+v", recs)
	}
}
