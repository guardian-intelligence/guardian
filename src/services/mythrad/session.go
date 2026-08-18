package main

import (
	"encoding/binary"
	"hash/fnv"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	webtransport "github.com/quic-go/webtransport-go"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/wire"
)

// Protocol event kinds the doorman needs by number (payload contents stay
// opaque to Go; docs/netcode.md). Kinds whose payload begins with the
// actor's dog id are authorization-checked at ingress.
const (
	evJoin         = 1
	evLeave        = 2
	evCheckIn      = 3
	evMoveTo       = 4
	evDayReset     = 5
	evEpochAdvance = 6
	evTerrainSet   = 7
	evBoostSet     = 8
	evClockSkip    = 9
	evRateSet      = 10
)

// dogIDFor derives the actor's dog id: the binding between OIDC subject
// and sim entity. Sessions may only submit intents about their own dog.
func dogIDFor(sub string) uint64 {
	f := fnv.New64a()
	f.Write([]byte(sub))
	return f.Sum64()
}

var sessionCount atomic.Int64

type session struct {
	sub      string
	role     string
	park     *authority
	sess     *webtransport.Session
	closeFn  func(string)
	out      chan []byte
	dogID    uint64
	openedAt time.Time

	closeOnce sync.Once
}

// intentBoundToActor reports whether the payload's leading dog id matches
// the session's own dog for kinds that carry one.
func intentBoundToActor(kind uint16, payload []byte, dogID uint64) bool {
	switch kind {
	case evJoin, evLeave, evCheckIn, evMoveTo, evBoostSet:
		return len(payload) >= 8 && binary.LittleEndian.Uint64(payload) == dogID
	default:
		return false // system kinds never arrive from sessions
	}
}

func (s *session) send(msg []byte) {
	select {
	case s.out <- msg:
	default:
		// Events are human-rate; a full buffer means the client stalled for
		// a long time. Close and let it rejoin through catch-up.
		mDrops.Inc()
		s.closeSession("stream backlog")
	}
}

func (s *session) sendReject(intentID uint64, reason uint32) {
	s.send(wire.EncodeReject(wire.Reject{Intent: intentID, Reason: reason}))
}

func (s *session) closeSession(why string) {
	s.closeOnce.Do(func() {
		log.Printf("wt session close: sub=%s role=%s park=%s reason=%q dur=%s",
			s.sub, s.role, s.park.name, why, time.Since(s.openedAt).Round(time.Millisecond))
		if s.closeFn != nil {
			s.closeFn(why)
		} else {
			s.sess.CloseWithError(4000, why)
		}
	})
}

// Doorman-level reject reasons live above the sim's code space.
const (
	rejectReadOnly = 100
	rejectNotYours = 101
)

func rejectReasonName(code uint32) string {
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
	case rejectReadOnly:
		return "read_only"
	case rejectNotYours:
		return "not_yours"
	}
	return strconv.FormatUint(uint64(code), 10)
}

func checkVerdict(park *authority, data []byte) ([]byte, bool) {
	chk, err := wire.DecodeCheck(data)
	if err != nil {
		return nil, false
	}
	ok, now := park.verdictFor(chk.Tick, chk.WH)
	_, cw := park.mods.client.get()
	_, pw := park.mods.park.get()
	result := "unknown"
	v := wire.Verdict{Tick: chk.Tick, Now: now, CTMS: chk.CTMS,
		CW: wire.ModuleWord(cw), PW: wire.ModuleWord(pw)}
	if ok != nil {
		v.Flags = wire.VerdictKnown
		if *ok {
			v.Flags |= wire.VerdictOK
			result = "ok"
		} else {
			result = "mismatch"
		}
	}
	mChecks.WithLabelValues(result).Inc()
	return wire.EncodeVerdict(v), true
}

// gameHandlers wires transport to parks and tickets.
type gameHandlers struct {
	parks        *parks
	tickets      *ticketMint
	maxSessions  int
	allowedParks map[string]bool
	anonMints    *anonLimiter
}
