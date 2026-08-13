package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	webtransport "github.com/quic-go/webtransport-go"
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
	msg, _ := json.Marshal(map[string]any{"type": "reject", "intent": intentID, "reason": reason})
	s.send(append(msg, '\n'))
}

func (s *session) closeSession(why string) {
	s.closeOnce.Do(func() {
		log.Printf("wt session close: sub=%s role=%s park=%s reason=%q dur=%s",
			s.sub, s.role, s.park.name, why, time.Since(s.openedAt).Round(time.Millisecond))
		s.sess.CloseWithError(4000, why)
	})
}

func (h *gameHandlers) handleSession(sess *webtransport.Session) {
	if n := sessionCount.Add(1); n > int64(h.maxSessions) {
		sessionCount.Add(-1)
		mHandshakes.WithLabelValues("capacity").Inc()
		sess.CloseWithError(4029, "at capacity")
		return
	}
	defer sessionCount.Add(-1)

	ctx := sess.Context()
	stream, err := sess.AcceptStream(ctx)
	if err != nil {
		mHandshakes.WithLabelValues("no_stream").Inc()
		return
	}
	stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	if !sc.Scan() {
		mHandshakes.WithLabelValues("no_hello").Inc()
		return
	}
	var hello struct {
		Type      string `json:"type"`
		Proto     int    `json:"proto"`
		Ticket    string `json:"ticket"`
		SinceSeq  int64  `json:"since_seq"`
		SinceTick uint64 `json:"since_tick"`
	}
	if json.Unmarshal(sc.Bytes(), &hello) != nil || hello.Type != "hello" || hello.Proto != 3 {
		mHandshakes.WithLabelValues("bad_hello").Inc()
		sess.CloseWithError(4400, "bad hello")
		return
	}
	tk, err := h.tickets.check(hello.Ticket)
	if err != nil {
		mHandshakes.WithLabelValues("bad_ticket").Inc()
		log.Printf("wt handshake rejected: bad ticket: remote=%s err=%v", sess.RemoteAddr(), err)
		sess.CloseWithError(4401, "bad ticket")
		return
	}
	stream.SetReadDeadline(time.Time{})

	park, err := h.parks.get(ctx, tk.Park)
	if err != nil {
		mHandshakes.WithLabelValues("park_unavailable").Inc()
		log.Printf("wt handshake rejected: park unavailable: sub=%s park=%s err=%v", tk.Sub, tk.Park, err)
		sess.CloseWithError(4503, "park unavailable")
		return
	}

	s := &session{
		sub: tk.Sub, role: tk.Role, park: park, sess: sess,
		out: make(chan []byte, 256), dogID: dogIDFor(tk.Sub),
		openedAt: time.Now(),
	}
	done := make(chan attachResult, 1)
	select {
	case park.attach <- attachReq{sess: s, sinceSeq: hello.SinceSeq, sinceTick: hello.SinceTick, done: done}:
	case <-ctx.Done():
		return
	}
	res := <-done
	if res.err != nil {
		sess.CloseWithError(4503, "park unavailable")
		return
	}
	mHandshakes.WithLabelValues("ok").Inc()
	log.Printf("wt session open: sub=%s role=%s park=%s remote=%s since_seq=%d",
		s.sub, s.role, park.name, sess.RemoteAddr(), hello.SinceSeq)
	mSessions.WithLabelValues(s.role).Inc()
	defer mSessions.WithLabelValues(s.role).Dec()
	defer park.detach(s)
	defer s.closeSession("bye")

	// Writer: welcome, catch-up material, then the live ordered stream.
	// Catch-up is built here — journal read and compression on this
	// session's time, never the park's tick loop; events that land in
	// s.out meanwhile are all newer than the attach position and drain
	// after, preserving order.
	go func() {
		write := func(b []byte) bool {
			stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, err := stream.Write(b)
			return err == nil
		}
		if !write(res.welcome) {
			return
		}
		for _, m := range park.catchupLines(hello.SinceSeq, hello.SinceTick, res) {
			if !write(m) {
				return
			}
		}
		for {
			select {
			case msg := <-s.out:
				if !write(msg) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go s.datagramLoop(ctx)

	// Reader: intents and resyncs, token-bucket limited. A spectator's
	// write intents die here — same protocol, no write authority.
	tokens := 40.0
	lastRefill := time.Now()
	for sc.Scan() {
		now := time.Now()
		tokens = min(40, tokens+20*now.Sub(lastRefill).Seconds())
		lastRefill = now
		if tokens < 1 {
			s.closeSession("rate limited")
			return
		}
		tokens--
		var m struct {
			Type string `json:"type"`
			ID   uint64 `json:"id"`
			Kind uint16 `json:"kind"`
			P    []byte `json:"p"`
			Have int64  `json:"have"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "intent":
			if s.role != "player" {
				s.sendReject(m.ID, rejectReadOnly)
				log.Printf("intent rejected: actor=%s kind=%d intent=%d reason=read_only", s.sub, m.Kind, m.ID)
				mIntentsRejected.WithLabelValues("read_only").Inc()
				continue
			}
			if !intentBoundToActor(m.Kind, m.P, s.dogID) {
				s.sendReject(m.ID, rejectNotYours)
				log.Printf("intent rejected: actor=%s kind=%d intent=%d reason=not_yours", s.sub, m.Kind, m.ID)
				mIntentsRejected.WithLabelValues("not_yours").Inc()
				continue
			}
			park.stageIntent(s, m.ID, m.Kind, m.P)
		case "resync":
			mResyncs.Inc()
			select {
			case park.attach <- attachReq{sess: s, resync: true, done: make(chan attachResult, 1)}:
			case <-ctx.Done():
				return
			}
		}
	}

	// A player dropping off the wire leaves the park: the departure is a
	// journal event like any other (the future check-out/timeout design
	// replaces this, not connection state).
	if s.role == "player" {
		var p [8]byte
		binary.LittleEndian.PutUint64(p[:], s.dogID)
		park.stageIntent(s, 0, evLeave, p[:])
	}
}

// Doorman-level reject reasons live above the sim's code space.
const (
	rejectReadOnly = 100
	rejectNotYours = 101
)

// rejectReasonName maps a reject code — the sim's own space plus the
// doorman's — to the label carried by logs and metrics.
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

func (s *session) datagramLoop(ctx context.Context) {
	for {
		data, err := s.sess.ReceiveDatagram(ctx)
		if err != nil {
			return
		}
		var m struct {
			Type string `json:"type"`
			Tick uint64 `json:"tick"`
			WH   string `json:"wh"`
			CT   int64  `json:"ct"`
		}
		if json.Unmarshal(data, &m) != nil || m.Type != "check" {
			continue
		}
		wh, err := strconv.ParseUint(m.WH, 16, 64)
		if err != nil {
			continue
		}
		ok, now := s.park.verdictFor(m.Tick, wh)
		_, cw := s.park.mods.client.get()
		_, pw := s.park.mods.park.get()
		result := "unknown"
		v := map[string]any{"type": "verdict", "tick": m.Tick, "now": now, "ct": m.CT,
			"cw": cw, "pw": pw}
		if ok != nil {
			v["ok"] = *ok
			if *ok {
				result = "ok"
			} else {
				result = "mismatch"
			}
		}
		mChecks.WithLabelValues(result).Inc()
		msg, _ := json.Marshal(v)
		if s.sess.SendDatagram(msg) == nil {
			mDgSent.Inc()
		} else {
			mDgErrors.Inc()
		}
	}
}

// gameHandlers wires transport to parks and tickets.
type gameHandlers struct {
	parks        *parks
	tickets      *ticketMint
	maxSessions  int
	allowedParks map[string]bool
	anonMints    *anonLimiter
}
