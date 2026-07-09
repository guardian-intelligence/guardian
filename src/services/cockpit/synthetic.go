package main

import "math/rand/v2"

// synthGen emits realistic fake node telemetry: an idle baseline around
// 5-15% CPU with tick-level jitter, occasional multi-second spikes to
// 60-90%, and slowly drifting memory. Deterministic for a given seed — the
// CI test substrate and the frontend's design-time stub share exact series.
type synthGen struct {
	rng         *rand.Rand
	cpu         float64
	mem         float64
	spikeTicks  int
	spikeTarget float64
}

func newSynthGen(seed uint64) *synthGen {
	return &synthGen{
		rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		cpu: 10,
		mem: 55,
	}
}

// next advances one 100 ms tick.
func (g *synthGen) next() (cpuBp, memBp uint32, ok bool) {
	target := 5 + 10*g.rng.Float64()
	if g.spikeTicks > 0 {
		g.spikeTicks--
		target = g.spikeTarget
	} else if g.rng.Float64() < 1.0/120 { // a spike roughly every 12 s
		g.spikeTicks = 20 + g.rng.IntN(40) // 2-6 s
		g.spikeTarget = 60 + 30*g.rng.Float64()
	}
	// First-order lag toward the target plus white jitter: fast enough to
	// look alive at 10 Hz, smooth enough to look like a machine.
	g.cpu += (target-g.cpu)*0.35 + (g.rng.Float64()-0.5)*3
	g.cpu = clamp(g.cpu, 0, 100)
	g.mem = clamp(g.mem+(g.rng.Float64()-0.5)*0.05, 35, 75)
	return uint32(g.cpu * 100), uint32(g.mem * 100), true
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
