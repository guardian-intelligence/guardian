package chunkie

import (
	"log"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// session is one attached replica: the chunk side of a gateway-relayed
// WebTransport session.
type session struct {
	sub      string
	role     string
	chunk     *authority
	closeFn  func(string)
	out      chan []byte
	actorID    uint64
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
	s.send(codec.EncodeReject(codec.Reject{Intent: intentID, Reason: reason}))
}

func (s *session) closeSession(why string) {
	s.closeOnce.Do(func() {
		log.Printf("wt session close: sub=%s role=%s chunk=%s reason=%q dur=%s",
			s.sub, s.role, s.chunk.name, why, time.Since(s.openedAt).Round(time.Millisecond))
		if s.closeFn != nil {
			s.closeFn(why)
		}
	})
}

func checkVerdict(chunk *authority, data []byte) ([]byte, bool) {
	chk, err := codec.DecodeCheck(data)
	if err != nil {
		return nil, false
	}
	ok, now := chunk.verdictFor(chk.Tick, chk.WH)
	_, cw := chunk.mods.client.Get()
	_, pw := chunk.mods.sim.Get()
	result := "unknown"
	v := codec.Verdict{Sub: chk.Sub, Lineage: 0, Tick: chk.Tick, Now: now, CTMS: chk.CTMS,
		CW: codec.ModuleWord(cw), PW: codec.ModuleWord(pw)}
	if ok != nil {
		v.Flags = codec.VerdictKnown
		if *ok {
			v.Flags |= codec.VerdictOK
			result = "ok"
		} else {
			result = "mismatch"
		}
	}
	mChecks.WithLabelValues(result).Inc()
	return codec.EncodeVerdict(v), true
}
