// Package wum is Wake Up Mythra's game vocabulary on the server side:
// the event kind numbers, the actor binding, the reject-reason names, and
// the genesis content artifact. Payload contents stay opaque to Go — the
// park module owns the rules — so this package is deliberately thin, and
// it is the only place the transport packages learn anything WUM-shaped.
package wum

import (
	"encoding/binary"
	_ "embed"
	"hash/fnv"
	"strconv"
)

// Event kinds, as the park module numbers them (docs/netcode.md). Kinds
// whose payload begins with the actor's dog id are authorization-checked
// at ingress.
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

// DogIDFor derives the actor's dog id: the binding between OIDC subject
// and sim entity. Sessions may only submit intents about their own dog.
func DogIDFor(sub string) uint64 {
	f := fnv.New64a()
	f.Write([]byte(sub))
	return f.Sum64()
}

// IntentBoundToActor reports whether the payload's leading dog id matches
// the session's own dog for kinds that carry one.
func IntentBoundToActor(kind uint16, payload []byte, dogID uint64) bool {
	switch kind {
	case EvJoin, EvLeave, EvCheckIn, EvMoveTo, EvBoostSet:
		return len(payload) >= 8 && binary.LittleEndian.Uint64(payload) == dogID
	default:
		return false // system kinds never arrive from sessions
	}
}

// ActionName is deliberately bounded: a client-controlled numeric kind
// must never become unbounded metric cardinality. Kept aligned with
// packages/wum-client/src/actions.ts; unknown attempts share one bucket.
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
