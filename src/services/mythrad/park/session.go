package park

import (
	"log"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/wire"
)

// session is one attached replica: the park side of a gateway-relayed
// WebTransport session.
type session struct {
	sub      string
	role     string
	park     *authority
	closeFn  func(string)
	out      chan []byte
	dogID    uint64
	openedAt time.Time

	closeOnce sync.Once
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
		}
	})
}

func checkVerdict(park *authority, data []byte) ([]byte, bool) {
	chk, err := wire.DecodeCheck(data)
	if err != nil {
		return nil, false
	}
	ok, now := park.verdictFor(chk.Tick, chk.WH)
	_, cw := park.mods.client.Get()
	_, pw := park.mods.park.Get()
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
