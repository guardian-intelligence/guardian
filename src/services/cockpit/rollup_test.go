package main

import (
	"testing"

	cockpitv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1"
)

func TestRollupRowsFoldsOneFrame(t *testing.T) {
	base := int64(1_700_000_000_000)
	ticks := make([]tick, 0, ticksPerFrame)
	// CPU walks 1000..1900 bp; memory is the frame's last value.
	for i := 0; i < ticksPerFrame; i++ {
		ticks = append(ticks, tick{
			tsMs:  base + int64(i)*tickIntervalMs,
			cpuBp: uint32(1000 + 100*i),
			memBp: uint32(4000 + i),
		})
	}
	frame := encodeFrame(base, []encodeNode{
		{index: 0, name: "ash-earth", keyframe: true, ticks: ticks},
	})

	dec := newFrameDecoder()
	series, err := dec.decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows := rollupRows(series)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.node != "ash-earth" || r.tsMs != base {
		t.Fatalf("identity = %q@%d, want ash-earth@%d", r.node, r.tsMs, base)
	}
	if r.cpuMinBp != 1000 || r.cpuMaxBp != 1900 {
		t.Fatalf("min/max = %d/%d, want 1000/1900", r.cpuMinBp, r.cpuMaxBp)
	}
	// avg of 1000..1900 step 100 = 1450
	if r.cpuAvgBp != 1450 {
		t.Fatalf("avg = %d, want 1450", r.cpuAvgBp)
	}
	if r.memUsedBp != 4009 {
		t.Fatalf("mem = %d, want the frame's last value 4009", r.memUsedBp)
	}
}

func TestRollupRowsChainsDeltaFrames(t *testing.T) {
	base := int64(1_700_000_000_000)
	kfTicks := []tick{{tsMs: base, cpuBp: 500, memBp: 3000}}
	deltaTicks := []tick{
		{tsMs: base + 1000, cpuBp: 700, memBp: 3100},
		{tsMs: base + 1100, cpuBp: 300, memBp: 3100},
	}
	dec := newFrameDecoder()
	kf, err := dec.decode(encodeFrame(base, []encodeNode{
		{index: 3, name: "ash-wind", keyframe: true, ticks: kfTicks},
	}))
	if err != nil {
		t.Fatalf("decode keyframe: %v", err)
	}
	if rows := rollupRows(kf); len(rows) != 1 || rows[0].cpuAvgBp != 500 {
		t.Fatalf("keyframe rows = %+v, want one row with avg 500", rows)
	}
	// The delta series carries no name; the decoder supplies it from the
	// keyframe binding, so the rollup still labels the row.
	rows := rollupRows(mustDecode(t, dec, encodeFrame(base+1000, []encodeNode{
		{index: 3, prevCpuBp: 500, ticks: deltaTicks},
	})))
	if len(rows) != 1 {
		t.Fatalf("delta rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.node != "ash-wind" {
		t.Fatalf("node = %q, want ash-wind from the keyframe binding", r.node)
	}
	if r.cpuMinBp != 300 || r.cpuMaxBp != 700 || r.cpuAvgBp != 500 {
		t.Fatalf("min/max/avg = %d/%d/%d, want 300/700/500", r.cpuMinBp, r.cpuMaxBp, r.cpuAvgBp)
	}
}

func TestRollupRowsSkipsUnnamedSeries(t *testing.T) {
	// A decoder that never saw the keyframe for a node has no name binding;
	// rollupRows must skip rather than write an unlabeled row. Fabricate the
	// decoded form directly — decode() itself rejects delta-before-keyframe.
	rows := rollupRows([]nodeSeries{
		{node: 9, name: "", baseTsMs: 1, cpuBp: []uint32{1, 2}, memBp: 3},
	})
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none for an unnamed series", rows)
	}
}

func mustDecode(t *testing.T, d *frameDecoder, f *cockpitv1.Frame) []nodeSeries {
	t.Helper()
	series, err := d.decode(f)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return series
}
