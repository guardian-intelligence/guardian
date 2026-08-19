package wum

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/guardian-intelligence/guardian/src/chunkies/gametest"
)

// The committed behavior artifacts are the game: the same bytes the prod
// ConfigMap mounts. The framework embeds nothing, so the conformance run
// reads them from the deploy tree.
func committedModule(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../deploy/prod/behavior/" + name)
	if err != nil {
		t.Fatalf("committed behavior artifact: %v", err)
	}
	return b
}

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
		Sim:     committedModule(t, "sim.wasm"),
		Modules: map[string][]byte{"client": committedModule(t, "client.wasm")},
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
	})
}
