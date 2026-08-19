// Package wum is Wake Up Mythra's game vocabulary on the server side:
// the event kind numbers, the actor binding, and the genesis content
// artifact. The framework never imports this package — the running host
// learns the same facts from game.conf (the game manifest it mounts) —
// so the constants here serve WUM's own code and tests, and game.conf is
// held in lockstep by TestManifestMatchesVocabulary.
package wum

import (
	_ "embed"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// Player event kinds, as the park module numbers them. The actor rides
// the SimEvent envelope; these numbers predate the framework kind-range
// convention (0x0100+ for game kinds) and are grandfathered — the
// journal's history is written in them. The system kinds (day_reset,
// epoch_advance, content_set, clock_skip, rate_set = 5..7, 9, 10) are
// framework constants now: codec.Kind*.
const (
	EvJoin         = 1
	EvLeave        = 2
	EvCheckIn      = 3
	EvMoveTo       = 4
	EvDayReset     = codec.KindDayReset
	EvEpochAdvance = codec.KindEpochAdvance
	EvTerrainSet   = codec.KindContentSet
	EvBoostSet     = 8
	EvClockSkip    = codec.KindClockSkip
	EvRateSet      = codec.KindRateSet
)

// FixtureTerrain is the world every brand-new park is born with until
// procedural generation exists. Committed bytes (diff-tested against the
// generator) so park identity can only change through an explicit refresh.
//
//go:embed fixture_park.bin
var FixtureTerrain []byte

// DogIDFor is WUM's name for the protocol's actor id: the binding between
// OIDC subject and sim entity. Sessions may only act as their own dog,
// which the gateway enforces game-blind against the intent envelope.
func DogIDFor(sub string) uint64 {
	return codec.ActorFor(sub)
}

