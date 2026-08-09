package main

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func decodeDelta(t *testing.T, buf []byte) (seq uint32, off, start, count uint16, st uint32, codes []byte) {
	t.Helper()
	if buf[0] != deltaMagic {
		t.Fatalf("magic = %#x", buf[0])
	}
	seq = binary.LittleEndian.Uint32(buf[1:])
	off = binary.LittleEndian.Uint16(buf[5:])
	start = binary.LittleEndian.Uint16(buf[7:])
	count = binary.LittleEndian.Uint16(buf[9:])
	st = binary.LittleEndian.Uint32(buf[11:])
	for i := 0; i < int(count); i++ {
		b := buf[deltaHeader+i/2]
		if i%2 == 0 {
			codes = append(codes, b&0x0F)
		} else {
			codes = append(codes, b>>4)
		}
	}
	return
}

func TestDeltaRoundTrip(t *testing.T) {
	kf := &roomKF{seq: 7, off: 3}
	moves := [][2]int8{{-1, -1}, {0, 0}, {1, 0}, {0, 1}, {1, 1}, {-1, 1}, {0, -1}}
	for i, m := range moves {
		kf.roster = append(kf.roster, &Player{ID: fmt.Sprintf("d%d", i), Room: "park", dX: m[0], dY: m[1]})
	}
	kf.roster = append(kf.roster, &Player{ID: "left", Room: "", dX: 1, dY: 1})

	chunks := encodeDeltas(kf, 123456, "park")
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	seq, off, start, count, st, codes := decodeDelta(t, chunks[0])
	if seq != 7 || off != 3 || start != 0 || int(count) != len(kf.roster) || st != 123456 {
		t.Fatalf("header = seq %d off %d start %d count %d st %d", seq, off, start, count, st)
	}
	for i, m := range moves {
		code := codes[i]
		dx, dy := int(code/3)-1, int(code%3)-1
		if int8(dx) != m[0] || int8(dy) != m[1] {
			t.Fatalf("dog %d: code %d -> (%d,%d), want (%d,%d)", i, code, dx, dy, m[0], m[1])
		}
	}
	if codes[len(codes)-1] != deltaGone {
		t.Fatalf("departed dog code = %d, want %d", codes[len(codes)-1], deltaGone)
	}
}

func TestDeltaChunkingAndBudget(t *testing.T) {
	kf := &roomKF{seq: 1}
	for i := 0; i < 2500; i++ {
		kf.roster = append(kf.roster, &Player{ID: fmt.Sprintf("d%d", i), Room: "park"})
	}
	chunks := encodeDeltas(kf, 1, "park")
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	// Every chunk must fit the QUIC datagram budget that motivated proto 2.
	for i, c := range chunks {
		if len(c) > 1150 {
			t.Fatalf("chunk %d is %dB, exceeds datagram budget", i, len(c))
		}
	}
	_, _, start0, count0, _, _ := decodeDelta(t, chunks[0])
	_, _, start1, count1, _, _ := decodeDelta(t, chunks[1])
	if start0 != 0 || int(count0) != deltaChunkDogs || int(start1) != deltaChunkDogs || int(count0)+int(count1) != 2500 {
		t.Fatalf("chunk spans = [%d+%d, %d+%d]", start0, count0, start1, count1)
	}
	// A 2000-dog park — the target load — must be a single datagram.
	kf.roster = kf.roster[:2000]
	if chunks := encodeDeltas(kf, 1, "park"); len(chunks) != 1 || len(chunks[0]) > 1150 {
		t.Fatalf("2000 dogs: %d chunks, first %dB", len(chunks), len(chunks[0]))
	}
}
