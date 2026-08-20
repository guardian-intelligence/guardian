package chunkie

// The game vocabulary: everything the authority host needs to know about
// a game that is not carried by the module artifacts themselves. It is
// pure data — kind numbers and bounded name tables — delivered as a
// mounted manifest file, so the framework binary stays game-blind and a
// game arrives entirely as content (modules, genesis artifact, manifest).
//
// Format, one directive per line, blank lines and #-comments allowed:
//
//	depart <kind>            event staged when a player session disconnects
//	action <kind> <name>     bounded metric/span name for a player intent kind
//	reject <code> <name>     bounded name for a sim reject code
//	legacy_actor <kind> <len> pre-v5 journal rows of <kind> whose payload is
//	                          exactly <len> bytes carry the actor id as an
//	                          8-byte prefix (dies with the Postgres journal)
//	consequential <kind>     facts that must survive world loss: routed via
//	                         the control-plane ledger and re-injected
//	                         exactly-once by id during recovery rungs 5–6 —
//	                         the game's one DR-visible declaration
//
// Boundedness matters: kind and code are client-controlled numbers, so
// only names from these tables (plus one shared unknown bucket) may ever
// become metric labels.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

type Vocab struct {
	// DepartKind is staged as a system-lifecycle intent when a player
	// session disconnects; zero means the game has no departure event.
	DepartKind uint16
	// Actions and Rejects are the bounded name tables for observability.
	Actions map[uint16]string
	Rejects map[uint32]string
	// LegacyActorPrefix marks journal payload eras per kind: a row whose
	// payload has exactly the recorded length predates the v5 flag day and
	// carries its actor id as a payload prefix.
	LegacyActorPrefix map[uint16]int
	// Consequential declares the kinds whose facts must survive world
	// loss. The checkpoint owns everything the sim computed; the
	// control-plane ledger owns consequential facts; the boundary
	// between them is event re-injection, never shared state. A kind
	// declared here is routed via the ledger and re-injected
	// exactly-once by id when a recovery rewinds lineage.
	Consequential map[uint16]bool
}

// actionName is deliberately bounded: unknown kinds share one bucket.
func (v *Vocab) actionName(kind uint16) string {
	if name, ok := v.Actions[kind]; ok {
		return name
	}
	return "unknown"
}

func (v *Vocab) rejectName(code uint32) string {
	if name, ok := v.Rejects[code]; ok {
		return name
	}
	switch code {
	case codec.RejectReadOnly:
		return "read_only"
	case codec.RejectNotYours:
		return "not_yours"
	}
	// Codes outside the tables share the unknown bucket: a mounted module
	// whose code space outgrew its manifest must not mint unbounded metric
	// labels. Logs carry the numeric code alongside the name.
	return "unknown"
}

// eventActor resolves a journal event to its SimEvent form across the two
// payload eras. Rows written before the v5 flag day carried the actor id
// as a payload prefix on the kinds the manifest lists; rows written after
// carry the v5 payload and name the subject in the actor column. The eras
// are distinguished by payload length, which the two encodings never
// share for any listed kind.
func (v *Vocab) eventActor(kind uint16, actor string, payload []byte) (uint64, []byte) {
	// Framework system events are authority-minted and never carry an
	// actor, whatever a row's actor column says — a backfilled or rogue
	// writer's name must not turn into an actor id the sim rejects.
	switch kind {
	case codec.KindDayReset, codec.KindEpochAdvance, codec.KindContentSet,
		codec.KindClockSkip, codec.KindRateSet:
		return 0, payload
	}
	if n, ok := v.LegacyActorPrefix[kind]; ok && len(payload) == n {
		return binary.LittleEndian.Uint64(payload[:8]), payload[8:]
	}
	if actor == "" || actor == "system" {
		return 0, payload
	}
	return codec.ActorFor(actor), payload
}

func parseVocab(r io.Reader) (Vocab, error) {
	v := Vocab{Actions: map[uint16]string{}, Rejects: map[uint32]string{}, LegacyActorPrefix: map[uint16]int{}, Consequential: map[uint16]bool{}}
	scanner := bufio.NewScanner(r)
	line := 0
	num := func(s string, bits int) (uint64, error) {
		n, err := strconv.ParseUint(s, 10, bits)
		if err != nil {
			return 0, fmt.Errorf("line %d: %q is not a base-10 u%d", line, s, bits)
		}
		return n, nil
	}
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		bad := func() (Vocab, error) {
			return Vocab{}, fmt.Errorf("line %d: unrecognized directive %q", line, text)
		}
		switch fields[0] {
		case "depart":
			if len(fields) != 2 {
				return bad()
			}
			kind, err := num(fields[1], 16)
			if err != nil {
				return Vocab{}, err
			}
			v.DepartKind = uint16(kind)
		case "action":
			if len(fields) != 3 {
				return bad()
			}
			kind, err := num(fields[1], 16)
			if err != nil {
				return Vocab{}, err
			}
			v.Actions[uint16(kind)] = fields[2]
		case "reject":
			if len(fields) != 3 {
				return bad()
			}
			code, err := num(fields[1], 32)
			if err != nil {
				return Vocab{}, err
			}
			v.Rejects[uint32(code)] = fields[2]
		case "consequential":
			if len(fields) != 2 {
				return bad()
			}
			kind, err := num(fields[1], 16)
			if err != nil {
				return Vocab{}, err
			}
			v.Consequential[uint16(kind)] = true
		case "legacy_actor":
			if len(fields) != 3 {
				return bad()
			}
			kind, err := num(fields[1], 16)
			if err != nil {
				return Vocab{}, err
			}
			n, err := num(fields[2], 16)
			if err != nil {
				return Vocab{}, err
			}
			if n < 8 {
				return Vocab{}, fmt.Errorf("line %d: legacy_actor length %d cannot hold an 8-byte prefix", line, n)
			}
			v.LegacyActorPrefix[uint16(kind)] = int(n)
		default:
			return bad()
		}
	}
	if err := scanner.Err(); err != nil {
		return Vocab{}, err
	}
	return v, nil
}

// loadVocab reads the game manifest. An empty path is a bare vocabulary
// (no departure event, unknown-bucket names, v5-only journal rows) — legal
// for a game that needs none of it. A path that fails to read or parse is
// a boot error: serving a game while blind to its vocabulary would journal
// departures under the wrong kind and mislabel every metric.
func loadVocab(path string) (Vocab, error) {
	if path == "" {
		return Vocab{Actions: map[uint16]string{}, Rejects: map[uint32]string{}, LegacyActorPrefix: map[uint16]int{}, Consequential: map[uint16]bool{}}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return Vocab{}, err
	}
	defer f.Close()
	return parseVocab(f)
}
