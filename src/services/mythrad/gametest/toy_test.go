package gametest

import (
	"encoding/binary"
	"os"
	"testing"
)

// The toy reference game through the same suite WUM passes. This is the
// framework's own conformance run: it proves the chunkies-abi trait, the
// export_simulation! macro, and the v2 SimEvent envelope carry a game
// with zero WUM in the loop — and it is the complete example a new game
// copies. Content-free: no genesis artifact, no fetch dance.
func TestToyGameConformance(t *testing.T) {
	module, err := os.ReadFile("../sim/shared/toy.wasm")
	if err != nil {
		t.Fatalf("built toy module: %v", err)
	}

	// Kind numbers mirror sim/shared/toy/src/lib.rs — by value, not by
	// import: the suite consumes artifacts, never game source.
	const (
		kJoin         = 0x0100
		kMove         = 0x0101
		kClockSkip    = 0x0009
		kEpochAdvance = 0x0006
		kRateSet      = 0x000A
	)
	move := func(d int32) []byte {
		return binary.LittleEndian.AppendUint32(nil, uint32(d))
	}

	Run(t, Game{
		Park: module,
		Corpus: []Event{
			{Kind: kJoin, Actor: 0xA11CE},
			{Kind: kJoin, Actor: 0xB0B},
			{Kind: kJoin, Actor: 0xCA401},
			{Kind: kMove, Actor: 0xA11CE, Payload: move(5)},
			{Kind: kMove, Actor: 0xB0B, Payload: move(-8)},
			{Kind: kMove, Actor: 0xCA401, Payload: move(1)},
			{Kind: kMove, Actor: 0xDEAD, Payload: move(3)}, // absent: a valid reject
		},
		System: System{
			RateSet:      kRateSet,
			ClockSkip:    kClockSkip,
			EpochAdvance: kEpochAdvance,
		},
	})
}
