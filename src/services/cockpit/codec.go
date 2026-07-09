package main

import (
	"fmt"

	cockpitv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1"
)

const (
	// tickIntervalMs is the sampling period; tick timestamps inside a frame
	// are implicit multiples of it from base_ts_ms.
	tickIntervalMs = 100
	// ticksPerFrame is the nominal tick count of a 1 s flush.
	ticksPerFrame = 10
	// keyframeEvery is the resync cadence: every 10th frame re-anchors
	// absolutes and node names.
	keyframeEvery = 10
	// maxSeriesTicks bounds one series so a stalled flush can never emit an
	// unbounded frame; a node with more pending ticks restarts from a
	// keyframe instead.
	maxSeriesTicks = 3 * ticksPerFrame
)

// tick is one 100 ms sample of a node, in basis points.
type tick struct {
	tsMs  int64
	cpuBp uint32
	memBp uint32
}

// encodeNode is one node's contribution to a frame.
type encodeNode struct {
	index uint32
	name  string
	// keyframe embeds the first tick's absolute CPU (and the node name);
	// otherwise the first delta chains off prevCpuBp.
	keyframe  bool
	prevCpuBp uint32
	// ticks is oldest-first and non-empty.
	ticks []tick
}

// encodeFrame delta-codes one flush window into the wire Frame.
func encodeFrame(baseTsMs int64, nodes []encodeNode) *cockpitv1.Frame {
	f := &cockpitv1.Frame{BaseTsMs: uint64(baseTsMs)}
	for _, n := range nodes {
		s := &cockpitv1.NodeSeries{
			Node:      n.index,
			MemUsedBp: n.ticks[len(n.ticks)-1].memBp,
		}
		prev := n.prevCpuBp
		rest := n.ticks
		if n.keyframe {
			s.Name = n.name
			kf := n.ticks[0].cpuBp
			s.CpuKeyframeBp = &kf
			prev = kf
			rest = n.ticks[1:]
		}
		s.CpuDeltasBp = make([]int32, 0, len(rest))
		for _, t := range rest {
			s.CpuDeltasBp = append(s.CpuDeltasBp, int32(t.cpuBp)-int32(prev))
			prev = t.cpuBp
		}
		f.Nodes = append(f.Nodes, s)
	}
	return f
}

// nodeSeries is the decoded per-node content of one frame, mirroring the
// wire granularity: CPU per tick, memory once per frame.
type nodeSeries struct {
	node     uint32
	name     string
	baseTsMs int64
	cpuBp    []uint32
	memBp    uint32
}

// frameDecoder reconstructs absolute tick series from the delta-coded
// stream. It is the reference for the browser client and the round-trip
// proof that encoding is lossless.
type frameDecoder struct {
	names map[uint32]string
	last  map[uint32]uint32
}

func newFrameDecoder() *frameDecoder {
	return &frameDecoder{names: map[uint32]string{}, last: map[uint32]uint32{}}
}

func (d *frameDecoder) decode(f *cockpitv1.Frame) ([]nodeSeries, error) {
	out := make([]nodeSeries, 0, len(f.GetNodes()))
	for _, s := range f.GetNodes() {
		ns := nodeSeries{
			node:     s.GetNode(),
			baseTsMs: int64(f.GetBaseTsMs()),
			memBp:    s.GetMemUsedBp(),
		}
		var cur int64
		if s.CpuKeyframeBp != nil {
			if s.GetName() != "" {
				d.names[s.GetNode()] = s.GetName()
			}
			cur = int64(s.GetCpuKeyframeBp())
			if cur > 10000 {
				return nil, fmt.Errorf("node %d: keyframe cpu %d out of range", s.GetNode(), cur)
			}
			ns.cpuBp = append(ns.cpuBp, uint32(cur))
		} else {
			last, ok := d.last[s.GetNode()]
			if !ok {
				return nil, fmt.Errorf("node %d: delta series before any keyframe", s.GetNode())
			}
			cur = int64(last)
		}
		ns.name = d.names[s.GetNode()]
		for _, delta := range s.GetCpuDeltasBp() {
			cur += int64(delta)
			if cur < 0 || cur > 10000 {
				return nil, fmt.Errorf("node %d: cpu %d out of range after delta", s.GetNode(), cur)
			}
			ns.cpuBp = append(ns.cpuBp, uint32(cur))
		}
		if len(ns.cpuBp) > 0 {
			d.last[s.GetNode()] = ns.cpuBp[len(ns.cpuBp)-1]
		}
		out = append(out, ns)
	}
	return out, nil
}
