// Package wum is Wake Up Mythra's game vocabulary on the server side:
// the event kind numbers, the actor binding, the reject-reason names, and
// the genesis content artifact. Payload contents stay opaque to Go — the
// park module owns the rules — so this package is deliberately thin, and
// it is the only place the transport packages learn anything WUM-shaped.
package wum

import (
	"encoding/binary"
	_ "embed"
	"strconv"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// Event kinds, as the park module numbers them (src/chunkies/README.md). The
// actor rides the SimEvent envelope; these numbers predate the framework
// kind-range convention (0x0100+ for game kinds) and are grandfathered —
// the journal's history is written in them.
const (
	EvJoin         = 1
	EvLeave        = 2
	EvCheckIn      = 3
	EvMoveTo       = 4
	EvDayReset     = 5
	EvEpochAdvance = 6
	EvTerrainSet   = 7
	EvBoostSet     = 8
	EvClockSkip    = 9
	EvRateSet      = 10
)

// Doorman-level reject reasons live above the sim's code space.
const (
	RejectReadOnly = 100
	RejectNotYours = 101
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

// EventActor resolves a journal event to its SimEvent form across the two
// payload eras. Rows written before the v5 flag day carried the dog id as
// a payload prefix on actor kinds; rows written after carry the v5
// payload and name the subject in the actor column. The eras are
// distinguished by payload length, which the two encodings never share
// for any kind. This shim dies with the Postgres journal.
func EventActor(kind uint16, actor string, payload []byte) (uint64, []byte) {
	oldLen := map[uint16]int{EvJoin: 8, EvLeave: 8, EvCheckIn: 8, EvMoveTo: 10, EvBoostSet: 9}
	switch kind {
	case EvJoin, EvLeave, EvCheckIn, EvMoveTo, EvBoostSet:
		if len(payload) == oldLen[kind] {
			return binary.LittleEndian.Uint64(payload[:8]), payload[8:]
		}
		if actor == "" || actor == "system" {
			return 0, payload
		}
		return DogIDFor(actor), payload
	default:
		return 0, payload
	}
}

// ActionName is deliberately bounded: a client-controlled numeric kind
// must never become unbounded metric cardinality. Kept aligned with
// src/games/wake-up-mythra/client/src/actions.ts; unknown attempts share one bucket.
func ActionName(kind uint16) string {
	switch kind {
	case EvJoin:
		return "join"
	case EvCheckIn:
		return "check_in"
	case EvMoveTo:
		return "move_to"
	case EvBoostSet:
		return "boost"
	default:
		return "unknown"
	}
}

func RejectReasonName(code uint32) string {
	switch code {
	case 1:
		return "encoding"
	case 2:
		return "present"
	case 3:
		return "absent"
	case 4:
		return "full"
	case 5:
		return "checked_in"
	case 6:
		return "kind"
	case 7:
		return "epoch"
	case 8:
		return "target"
	case 9:
		return "terrain"
	case 10:
		return "noop"
	case RejectReadOnly:
		return "read_only"
	case RejectNotYours:
		return "not_yours"
	}
	return strconv.FormatUint(uint64(code), 10)
}
