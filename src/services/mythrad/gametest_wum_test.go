package main

import (
	"encoding/binary"
	"testing"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/gametest"
)

// The WUM park module through the game-blind conformance suite: the
// committed artifacts, the genesis terrain, and a corpus of valid intents.
// The suite owns the properties (determinism, snapshot completeness,
// reject purity, system-event semantics); only the corpus and kind
// numbers here are WUM's.
func TestWUMGameConformance(t *testing.T) {
	dog := func(id uint64) []byte {
		var p [8]byte
		binary.LittleEndian.PutUint64(p[:], id)
		return p[:]
	}
	move := func(id uint64, node uint16) []byte {
		return binary.LittleEndian.AppendUint16(dog(id), node)
	}
	boost := func(id uint64, on byte) []byte {
		return append(dog(id), on)
	}
	alice, bob, carol := dogIDFor("alice"), dogIDFor("bob"), dogIDFor("carol")

	gametest.Run(t, gametest.Game{
		Park:    defaultParkModule,
		Modules: map[string][]byte{"client": defaultClientModule},
		Genesis: fixtureTerrain,
		Corpus: []gametest.Event{
			{Kind: evJoin, Payload: dog(alice)},
			{Kind: evJoin, Payload: dog(bob)},
			{Kind: evJoin, Payload: dog(carol)},
			{Kind: evCheckIn, Payload: dog(alice)},
			{Kind: evCheckIn, Payload: dog(bob)},
			{Kind: evMoveTo, Payload: move(alice, 1290)}, // (10, 10): open grass
			{Kind: evMoveTo, Payload: move(bob, 1290)},
			{Kind: evMoveTo, Payload: move(carol, 2580)},
			{Kind: evBoostSet, Payload: boost(alice, 1)},
			{Kind: evBoostSet, Payload: boost(alice, 0)},
			{Kind: evLeave, Payload: dog(bob)},
			{Kind: evDayReset, Payload: binary.LittleEndian.AppendUint32(nil, 1)},
		},
		System: gametest.System{
			RateSet:      evRateSet,
			ClockSkip:    evClockSkip,
			EpochAdvance: evEpochAdvance,
		},
	})
}
