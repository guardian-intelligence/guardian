package codec

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

//go:embed spec/vectors.txt
var vectorsTxt string

//go:embed spec/caps.txt
var capsTxt string

// The shared fixture values behind every vector. Byte positions are
// chosen adversarially: no two same-width fields share bytes, so a
// transposed pair cannot pass.
const (
	fxLineage    = 7
	fxGeneration = 3
	fxSub        = 0x0D0C0B0A
	fxEpoch      = 2
	fxSeq        = 9
	fxTick       = 1024
	fxHz         = 24
	fxWH         = 0xFEEDFACECAFEBEEF
	fxContent    = 0x0123456789ABCDEF
	fxCT         = 1700000000000
	fxIntent     = 0x1122334455667788
	fxActor      = 0xA1A2A3A4A5A6A7A8
	fxKind       = 0x0104
	fxSysKind    = 0x0009
	fxChunk      = 21
	fxOrdinal    = 5
)

func fxRec1() []byte {
	return AppendEventRecord(nil, fxIntent, fxKind, fxActor, []byte{0xDE, 0xAD, 0xBE, 0xEF})
}

func fxRec2() []byte {
	return AppendEventRecord(nil, SystemIntent, fxSysKind, 0, []byte{0x0A, 0x0B, 0x0C, 0x0D})
}

func fxRun() []byte { return append(fxRec1(), fxRec2()...) }

var fxZ = []byte{0xC0, 0xFF, 0xEE, 0x00}

func fxCheckpoint() Checkpoint {
	f := Checkpoint{
		Version: 1, Game: "wum", Chunk: "park-a",
		Lineage: fxLineage, Generation: fxGeneration,
		Seq: fxSeq, Tick: fxTick, Epoch: fxEpoch,
		WH: fxWH, Content: fxContent,
		Dedup: []DedupEntry{{Actor: fxActor, Intent: fxIntent}},
		State: fxZ,
	}
	for i := range f.CW {
		f.CW[i] = byte(0x40 + i)
		f.PW[i] = byte(0x60 + i)
	}
	return f
}

// encoders maps each vector name to this implementation's encoding of the
// shared fixture message.
var encoders = map[string]func() []byte{
	"hello": func() []byte {
		return EncodeHello(Hello{Proto: Proto, SinceLineage: fxLineage, SinceSeq: fxSeq, SinceTick: fxTick, Ticket: "T-9f"})
	},
	"intent": func() []byte {
		return EncodeIntent(fxIntent, fxKind, fxActor, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	},
	"resync":     func() []byte { return EncodeResync(Resync{Lineage: fxLineage, HaveSeq: fxSeq}) },
	"resync-neg": func() []byte { return EncodeResync(Resync{Lineage: fxLineage, HaveSeq: -1}) },
	"welcome": func() []byte {
		return EncodeWelcome(Welcome{
			Lineage: fxLineage, Generation: fxGeneration, Sub: fxSub, Epoch: fxEpoch,
			Seq: fxSeq, Tick: fxTick, Hz: fxHz, Role: RolePlayer, Content: fxContent, Chunk: "park-a",
		})
	},
	"tick":   func() []byte { return EncodeTick(fxTick, fxSeq, 2, fxRun()) },
	"reject": func() []byte { return EncodeReject(Reject{Intent: fxIntent, Reason: 101}) },
	"snapshot": func() []byte {
		return EncodeSnapshot(Snapshot{Lineage: fxLineage, Seq: fxSeq, Tick: fxTick, Epoch: fxEpoch, WH: fxWH, Content: fxContent, Z: fxZ})
	},
	"check": func() []byte { return EncodeCheck(Check{Sub: fxSub, Tick: fxTick, WH: fxWH, CTMS: fxCT}) },
	"verdict": func() []byte {
		return EncodeVerdict(Verdict{
			Sub: fxSub, Lineage: fxLineage, Tick: fxTick, Now: fxCT + 123, CTMS: fxCT,
			Flags: VerdictKnown | VerdictOK,
			CW:    ModuleWord("9abcdef0"), PW: ModuleWord("12345678"),
		})
	},
	"segment": func() []byte {
		return EncodeSegmentHeader(SegmentHeader{
			Version: SegmentVersion, Generation: fxGeneration, Ordinal: fxOrdinal,
			Chunks: []SegmentChunk{
				{Name: "park-a", Lineage: fxLineage, Epoch: fxEpoch, FirstTick: fxTick},
				{Name: "park-b", Lineage: 8, Epoch: 6, FirstTick: 2048},
			},
		})
	},
	"tickrec":    func() []byte { return AppendTickRecord(nil, fxChunk, fxTick, fxSeq, 2, fxRun(), fxWH) },
	"watermark":  func() []byte { return AppendWatermark(nil, fxChunk, fxTick) },
	"checkpoint": func() []byte { return EncodeCheckpoint(fxCheckpoint()) },
}

// decoders maps each vector name to a strict decode of a full vector (for
// frames: prefix + kind + payload), returning an error on any mismatch.
var decoders = map[string]func([]byte) error{
	"hello":      frameDecoder(KindHello, func(p []byte) error { _, err := DecodeHello(p); return err }),
	"intent":     frameDecoder(KindIntent, func(p []byte) error { _, err := DecodeIntent(p); return err }),
	"resync":     frameDecoder(KindResync, func(p []byte) error { _, err := DecodeResync(p); return err }),
	"resync-neg": frameDecoder(KindResync, func(p []byte) error { _, err := DecodeResync(p); return err }),
	"welcome":    frameDecoder(KindWelcome, func(p []byte) error { _, err := DecodeWelcome(p); return err }),
	"tick":       frameDecoder(KindTick, func(p []byte) error { _, _, err := DecodeTick(p); return err }),
	"reject":     frameDecoder(KindReject, func(p []byte) error { _, err := DecodeReject(p); return err }),
	"snapshot":   frameDecoder(KindSnapshot, func(p []byte) error { _, err := DecodeSnapshot(p); return err }),
	"check":      func(b []byte) error { _, err := DecodeCheck(b); return err },
	"verdict":    func(b []byte) error { _, err := DecodeVerdict(b); return err },
	"segment": func(b []byte) error {
		_, n, err := DecodeSegmentHeader(b)
		if err == nil && n != len(b) {
			return ErrBadPayload
		}
		return err
	},
	"tickrec":    recordDecoder,
	"watermark":  recordDecoder,
	"checkpoint": func(b []byte) error { _, err := DecodeCheckpoint(b); return err },
}

func frameDecoder(wantKind byte, payload func([]byte) error) func([]byte) error {
	return func(b []byte) error {
		r := NewReader(bytes.NewReader(b))
		kind, p, err := r.Next()
		if err != nil {
			return err
		}
		if kind != wantKind {
			return ErrBadPayload
		}
		return payload(p)
	}
}

func recordDecoder(b []byte) error {
	_, n, err := ReadRecord(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return ErrBadPayload
	}
	return nil
}

func parseVectors(t *testing.T) (good, bad map[string][]byte) {
	t.Helper()
	good, bad = map[string][]byte{}, map[string][]byte{}
	for lineNo, line := range strings.Split(vectorsTxt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, hexStr, ok := strings.Cut(line, " = ")
		if !ok {
			t.Fatalf("vectors.txt:%d: unparseable line %q", lineNo+1, line)
		}
		raw, err := hex.DecodeString(hexStr)
		if err != nil {
			t.Fatalf("vectors.txt:%d: bad hex: %v", lineNo+1, err)
		}
		if bang, isBad := strings.CutPrefix(name, "!"); isBad {
			bad[bang] = raw
		} else {
			good[name] = raw
		}
	}
	return good, bad
}

// TestVectors holds encode and decode to the shared spec: every vector
// must be reproduced byte-identically from the fixture message, and must
// decode cleanly; every !vector must fail decode.
func TestVectors(t *testing.T) {
	good, bad := parseVectors(t)
	for name, want := range good {
		enc, ok := encoders[name]
		if !ok {
			t.Errorf("vector %q has no encoder in this implementation", name)
			continue
		}
		if got := enc(); !bytes.Equal(got, want) {
			t.Errorf("%s: encoded\n  %x\nwant\n  %x", name, got, want)
		}
		if err := decoders[name](want); err != nil {
			t.Errorf("%s: decode failed: %v", name, err)
		}
	}
	for name, raw := range bad {
		// A malformed vector's base decoder is everything before the first
		// dash: "intent-elen-short" exercises the intent decoder.
		base := name
		if i := strings.IndexByte(name, '-'); i > 0 {
			base = name[:i]
		}
		dec, ok := decoders[base]
		if !ok {
			t.Errorf("malformed vector %q has no decoder for base %q", name, base)
			continue
		}
		if err := dec(raw); err == nil {
			t.Errorf("!%s: decode unexpectedly succeeded", name)
		}
	}
	// Every message this implementation encodes must be pinned by a vector
	// — an unpinned encoder is an escape hatch from the spec.
	for name := range encoders {
		if _, ok := good[name]; !ok {
			t.Errorf("encoder %q has no vector", name)
		}
	}
}

// TestIntentRecordIsSliceOfTick pins the protocol's central property: an
// accepted intent's payload bytes appear verbatim inside the tick batch —
// nothing between arrival and fan-out re-encodes them.
func TestIntentRecordIsSliceOfTick(t *testing.T) {
	good, _ := parseVectors(t)
	intent, tick, tickrec := good["intent"], good["tick"], good["tickrec"]
	r := NewReader(bytes.NewReader(intent))
	if _, p, err := r.Next(); err != nil || !bytes.Contains(tick, p) {
		t.Fatalf("intent record is not a verbatim slice of the tick frame (err %v)", err)
	} else if !bytes.Contains(tickrec, p) {
		t.Fatalf("intent record is not a verbatim slice of the TickRecord")
	}
}

// TestCaps holds this implementation's constants to spec/caps.txt.
func TestCaps(t *testing.T) {
	want := map[string]int{
		"PROTO":          Proto,
		"MAX_FRAME":      MaxFrameLen,
		"DG_MAX":         DatagramMax,
		"WAL_MAX_RECORD": MaxRecordLen,
		"WAL_MAX_CHUNKS": MaxSegmentChunks,
	}
	seen := 0
	for _, line := range strings.Split(capsTxt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, val, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("caps.txt: unparseable line %q", line)
		}
		n, err := strconv.Atoi(val)
		if err != nil {
			t.Fatalf("caps.txt: %s: %v", name, err)
		}
		have, known := want[name]
		if !known {
			t.Errorf("caps.txt names %q which this implementation does not define", name)
			continue
		}
		if have != n {
			t.Errorf("%s: implementation has %d, caps.txt says %d", name, have, n)
		}
		seen++
	}
	if seen != len(want) {
		t.Errorf("caps.txt pins %d values, implementation defines %d", seen, len(want))
	}
}
