package wum

import (
	"encoding/binary"
	"testing"

	"github.com/guardian-intelligence/guardian/src/chunkies/gametest"
	"github.com/guardian-intelligence/guardian/src/chunkies/mount"
)

// The WUM park module through the game-blind conformance suite: the
// committed artifacts, the genesis terrain, and a corpus of valid intents.
// The suite owns the properties (determinism, snapshot completeness,
// reject purity, system-event semantics); only the corpus and kind
// numbers here are WUM's.
func TestWUMGameConformance(t *testing.T) {
	move := func(node uint16) []byte {
		return binary.LittleEndian.AppendUint16(nil, node)
	}
	alice, bob, carol := DogIDFor("alice"), DogIDFor("bob"), DogIDFor("carol")

	gametest.Run(t, gametest.Game{
		Park:    mount.DefaultPark,
		Modules: map[string][]byte{"client": mount.DefaultClient},
		Genesis: FixtureTerrain,
		Corpus: []gametest.Event{
			{Kind: EvJoin, Actor: alice},
			{Kind: EvJoin, Actor: bob},
			{Kind: EvJoin, Actor: carol},
			{Kind: EvCheckIn, Actor: alice},
			{Kind: EvCheckIn, Actor: bob},
			{Kind: EvMoveTo, Actor: alice, Payload: move(1290)}, // (10, 10): open grass
			{Kind: EvMoveTo, Actor: bob, Payload: move(1290)},
			{Kind: EvMoveTo, Actor: carol, Payload: move(2580)},
			{Kind: EvBoostSet, Actor: alice, Payload: []byte{1}},
			{Kind: EvBoostSet, Actor: alice, Payload: []byte{0}},
			{Kind: EvLeave, Actor: bob},
			{Kind: EvDayReset, Payload: binary.LittleEndian.AppendUint32(nil, 1)},
		},
		System: gametest.System{
			RateSet:      EvRateSet,
			ClockSkip:    EvClockSkip,
			EpochAdvance: EvEpochAdvance,
		},
	})
}
