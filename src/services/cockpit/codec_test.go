package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	cockpitv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1"
)

// synthSeries produces n ticks on the 100 ms grid from the deterministic
// generator.
func synthSeries(seed uint64, baseTs int64, n int) []tick {
	g := newSynthGen(seed)
	out := make([]tick, n)
	for i := range out {
		cpu, mem, _ := g.next()
		out[i] = tick{tsMs: baseTs + int64(i*tickIntervalMs), cpuBp: cpu, memBp: mem}
	}
	return out
}

// TestEncodeDecodeRoundTrip proves the delta/keyframe coding is lossless: a
// synthetic series chunked into frames (keyframe every keyframeEvery) and
// decoded reproduces every CPU tick and every per-frame memory value
// exactly.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	const frames = 25
	series := synthSeries(42, 1_700_000_000_000, frames*ticksPerFrame)

	dec := newFrameDecoder()
	prev := uint32(0)
	var gotCpu []uint32
	for i := 0; i < frames; i++ {
		chunk := series[i*ticksPerFrame : (i+1)*ticksPerFrame]
		keyframe := i%keyframeEvery == 0
		f := encodeFrame(chunk[0].tsMs, []encodeNode{{
			index:     3,
			name:      "ash-earth",
			keyframe:  keyframe,
			prevCpuBp: prev,
			ticks:     chunk,
		}})
		prev = chunk[len(chunk)-1].cpuBp

		decoded, err := dec.decode(f)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(decoded) != 1 {
			t.Fatalf("frame %d: %d series, want 1", i, len(decoded))
		}
		s := decoded[0]
		if s.node != 3 || s.name != "ash-earth" {
			t.Fatalf("frame %d: identity = (%d, %q)", i, s.node, s.name)
		}
		if s.baseTsMs != chunk[0].tsMs {
			t.Fatalf("frame %d: baseTs = %d, want %d", i, s.baseTsMs, chunk[0].tsMs)
		}
		if want := chunk[len(chunk)-1].memBp; s.memBp != want {
			t.Fatalf("frame %d: mem = %d, want %d", i, s.memBp, want)
		}
		gotCpu = append(gotCpu, s.cpuBp...)
	}
	if len(gotCpu) != len(series) {
		t.Fatalf("decoded %d ticks, want %d", len(gotCpu), len(series))
	}
	for i, cpu := range gotCpu {
		if cpu != series[i].cpuBp {
			t.Fatalf("tick %d: cpu = %d, want %d", i, cpu, series[i].cpuBp)
		}
	}
}

// TestSteadyStateFrameSize pins the wire budget the format was designed
// for: a delta frame for one node stays within tens of bytes.
func TestSteadyStateFrameSize(t *testing.T) {
	series := synthSeries(7, 1_700_000_000_000, 2*ticksPerFrame)
	f := encodeFrame(series[ticksPerFrame].tsMs, []encodeNode{{
		index:     0,
		keyframe:  false,
		prevCpuBp: series[ticksPerFrame-1].cpuBp,
		ticks:     series[ticksPerFrame:],
	}})
	raw, err := proto.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 50 {
		t.Fatalf("steady-state frame is %d bytes, budget 50", len(raw))
	}
}

func TestDecodeDeltaBeforeKeyframeFails(t *testing.T) {
	dec := newFrameDecoder()
	f := &cockpitv1.Frame{
		BaseTsMs: 1000,
		Nodes:    []*cockpitv1.NodeSeries{{Node: 0, CpuDeltasBp: []int32{5}}},
	}
	if _, err := dec.decode(f); err == nil {
		t.Fatal("expected error for delta series with no prior keyframe")
	}
}

func TestDecodeRejectsOutOfRangeCpu(t *testing.T) {
	kf := uint32(9990)
	dec := newFrameDecoder()
	f := &cockpitv1.Frame{
		BaseTsMs: 1000,
		Nodes: []*cockpitv1.NodeSeries{{
			Node:          0,
			Name:          "n",
			CpuKeyframeBp: &kf,
			CpuDeltasBp:   []int32{20},
		}},
	}
	if _, err := dec.decode(f); err == nil {
		t.Fatal("expected error for cpu above 10000 bp")
	}
	under := &cockpitv1.Frame{
		BaseTsMs: 1000,
		Nodes: []*cockpitv1.NodeSeries{{
			Node:          0,
			Name:          "n",
			CpuKeyframeBp: proto.Uint32(5),
			CpuDeltasBp:   []int32{-6},
		}},
	}
	if _, err := newFrameDecoder().decode(under); err == nil {
		t.Fatal("expected error for cpu below 0 bp")
	}
}

// TestSyntheticGeneratorShape sanity-checks the design-time stub: values in
// range, deterministic per seed, and spikes actually occur.
func TestSyntheticGeneratorShape(t *testing.T) {
	a := synthSeries(9, 0, 1200) // 2 minutes
	b := synthSeries(9, 0, 1200)
	spiked := false
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("generator is not deterministic for a fixed seed")
		}
		if a[i].cpuBp > 10000 || a[i].memBp > 10000 {
			t.Fatalf("tick %d out of range: %+v", i, a[i])
		}
		if a[i].cpuBp > 5500 {
			spiked = true
		}
	}
	if !spiked {
		t.Fatal("expected at least one spike above 55% in 2 minutes")
	}
	if c := synthSeries(10, 0, 1200); c[100] == a[100] && c[200] == a[200] && c[300] == a[300] {
		t.Fatal("different seeds produced the same series")
	}
}
