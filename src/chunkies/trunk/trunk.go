// Package trunk is the authenticated internal transport between the
// gateway and a chunk backend. One TCP connection carries every
// attachment for its (gateway, backend) pair: the HMAC handshake happens
// once per connection, attachments open and close as multiplexed control
// messages, and a single ping probes the backend's liveness for all of
// them. That makes re-attaching to a different backend a control message
// rather than a redial — the seam cross-chunk transfer needs. Tick
// fan-out is publish-once: the backend broadcasts each committed tick
// once per connection, and the dial side replicates it to the local
// attachments subscribed to that chunk, splicing it into each unicast
// lane by seq.
package trunk

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

const (
	proxyMagic      = "CHUNKY03"
	proxyMaxPayload = 1 << 20

	KindOpen     = 1
	KindStream   = 2
	KindDatagram = 3
	KindClose    = 4
	KindPing     = 5
	KindPong     = 6
	// KindBroadcast rides backend to gateway only, SID 0: one committed
	// tick frame addressed to a (game, chunk) instead of an attachment.
	KindBroadcast = 7

	defaultPingEvery  = 5 * time.Second
	defaultPongWithin = 15 * time.Second
	defaultIdleAfter  = 60 * time.Second
	// acceptReadTimeout is the accept side's silence budget. It treats a
	// quiet connection as dead because the dial side's ping cadence
	// guarantees traffic, so it must comfortably exceed defaultPingEvery;
	// a Pool tuned to ping slower than this will lose idle connections.
	acceptReadTimeout = 30 * time.Second
)

var errAttachmentClosed = errors.New("trunk attachment closed")

// ErrIdle is the reason an attachmentless connection was reaped; owners
// can tell routine lifecycle from failure in their ConnDown hook.
var ErrIdle = errors.New("idle")

// Open carries one attachment's identity to the chunk. It rides the
// authenticated connection, so it needs no signature of its own.
type Open struct {
	Game      string
	Sub       string
	Chunk     string
	Role      string
	Remote    string
	SinceSeq  int64
	SinceTick uint64
}

// Msg is one demultiplexed proxy message. Ping and pong reuse the session
// id slot as an opaque token and never reach callers of ReadMessage.
type Msg struct {
	Kind    byte
	SID     uint64
	Payload []byte
}

// Conn is one authenticated proxy connection shared by many attachments.
type Conn struct {
	c       net.Conn
	writeMu sync.Mutex
	// readTimeout is set on the accept side only: the gateway's ping
	// cadence guarantees traffic on a live connection, so silence for the
	// whole window means the peer is gone.
	readTimeout time.Duration
}

func (p *Conn) Close() error { return p.c.Close() }

func ReadKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) < 32 {
		return nil, errors.New("internal key must contain at least 32 bytes")
	}
	return b, nil
}

// Accept authenticates an inbound gateway connection.
func Accept(c net.Conn, key []byte, now time.Time) (*Conn, error) {
	p := &Conn{c: c, readTimeout: acceptReadTimeout}
	if err := p.readHandshake(key, now); err != nil {
		c.Close()
		return nil, err
	}
	return p, nil
}

func (p *Conn) writeHandshake(key []byte) error {
	b := make([]byte, 0, 64)
	b = append(b, proxyMagic...)
	b = binary.LittleEndian.AppendUint64(b, uint64(time.Now().Unix()))
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	b = append(b, nonce[:]...)
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	b = append(b, mac.Sum(nil)...)
	p.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer p.c.SetWriteDeadline(time.Time{})
	var size [2]byte
	binary.LittleEndian.PutUint16(size[:], uint16(len(b)))
	if _, err := p.c.Write(size[:]); err != nil {
		return err
	}
	_, err := p.c.Write(b)
	return err
}

func (p *Conn) readHandshake(key []byte, now time.Time) error {
	p.c.SetReadDeadline(now.Add(5 * time.Second))
	defer p.c.SetReadDeadline(time.Time{})
	var size [2]byte
	if _, err := io.ReadFull(p.c, size[:]); err != nil {
		return err
	}
	n := int(binary.LittleEndian.Uint16(size[:]))
	if n != len(proxyMagic)+8+16+sha256.Size {
		return errors.New("bad proxy handshake size")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(p.c, b); err != nil {
		return err
	}
	body, sig := b[:n-sha256.Size], b[n-sha256.Size:]
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return errors.New("bad proxy signature")
	}
	if string(body[:len(proxyMagic)]) != proxyMagic {
		return errors.New("bad proxy magic")
	}
	// The freshness window bounds replay of a captured handshake. It
	// authenticates a whole connection rather than one attachment; the
	// real boundary is key possession on the cluster network, as before.
	ts := int64(binary.LittleEndian.Uint64(body[len(proxyMagic):]))
	if d := now.Sub(time.Unix(ts, 0)); d < -30*time.Second || d > 30*time.Second {
		return errors.New("stale proxy handshake")
	}
	return nil
}

// WriteMessage frames kind|sid|payload onto the shared connection. The
// deadline bounds how long one stalled peer can hold the write lock every
// other attachment shares.
func (p *Conn) WriteMessage(kind byte, sid uint64, payload []byte) error {
	if len(payload) > proxyMaxPayload {
		return errors.New("proxy payload too large")
	}
	// The stall clock starts before the lock: a slow peer shows up in
	// every other writer's wait, and that shared head-of-line time is the
	// histogram's subject.
	start := time.Now()
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer p.c.SetWriteDeadline(time.Time{})
	head := make([]byte, 13, 13+len(payload))
	binary.LittleEndian.PutUint32(head[:4], uint32(len(payload)+9))
	head[4] = kind
	binary.LittleEndian.PutUint64(head[5:], sid)
	_, err := p.c.Write(append(head, payload...))
	mWriteStall.Observe(time.Since(start).Seconds())
	if err == nil {
		countFrame("send", kind, 13+len(payload))
	}
	return err
}

// ReadMessage returns the next message, answering pings itself so both
// ends see liveness traffic without threading it through their loops.
func (p *Conn) ReadMessage() (Msg, error) {
	for {
		if p.readTimeout > 0 {
			p.c.SetReadDeadline(time.Now().Add(p.readTimeout))
		}
		var head [13]byte
		if _, err := io.ReadFull(p.c, head[:]); err != nil {
			return Msg{}, err
		}
		n := int(binary.LittleEndian.Uint32(head[:4]))
		if n < 9 || n > proxyMaxPayload+9 {
			return Msg{}, fmt.Errorf("bad proxy message size %d", n)
		}
		m := Msg{Kind: head[4], SID: binary.LittleEndian.Uint64(head[5:])}
		if n > 9 {
			m.Payload = make([]byte, n-9)
			if _, err := io.ReadFull(p.c, m.Payload); err != nil {
				return Msg{}, err
			}
		}
		countFrame("recv", m.Kind, n+4)
		if m.Kind == KindPing {
			if err := p.WriteMessage(KindPong, m.SID, nil); err != nil {
				return Msg{}, err
			}
			continue
		}
		return m, nil
	}
}

func (p *Conn) WriteStream(sid uint64, frame []byte) error {
	return p.WriteMessage(KindStream, sid, frame)
}

func (p *Conn) WriteDatagram(sid uint64, b []byte) error {
	return p.WriteMessage(KindDatagram, sid, b)
}

func (p *Conn) WriteClose(sid uint64, reason string) error {
	return p.WriteMessage(KindClose, sid, []byte(reason))
}

func appendProxyString(dst []byte, s string) ([]byte, error) {
	if len(s) > 4096 {
		return nil, errors.New("proxy string too long")
	}
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(s)))
	return append(dst, s...), nil
}

func takeProxyString(b []byte, at *int) (string, error) {
	if *at+2 > len(b) {
		return "", io.ErrUnexpectedEOF
	}
	n := int(binary.LittleEndian.Uint16(b[*at:]))
	*at += 2
	if *at+n > len(b) {
		return "", io.ErrUnexpectedEOF
	}
	s := string(b[*at : *at+n])
	*at += n
	return s, nil
}

func encodeOpen(open Open) ([]byte, error) {
	b := make([]byte, 0, 128)
	b = binary.LittleEndian.AppendUint64(b, uint64(open.SinceSeq))
	b = binary.LittleEndian.AppendUint64(b, open.SinceTick)
	var err error
	for _, s := range []string{open.Game, open.Sub, open.Chunk, open.Role, open.Remote} {
		b, err = appendProxyString(b, s)
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

func DecodeOpen(payload []byte) (Open, error) {
	if len(payload) < 16 {
		return Open{}, io.ErrUnexpectedEOF
	}
	open := Open{
		SinceSeq:  int64(binary.LittleEndian.Uint64(payload)),
		SinceTick: binary.LittleEndian.Uint64(payload[8:]),
	}
	at := 16
	for _, field := range []*string{&open.Game, &open.Sub, &open.Chunk, &open.Role, &open.Remote} {
		s, err := takeProxyString(payload, &at)
		if err != nil {
			return Open{}, err
		}
		*field = s
	}
	if at != len(payload) || open.Game == "" || open.Sub == "" || open.Chunk == "" || (open.Role != "player" && open.Role != "spectator") {
		return Open{}, errors.New("bad proxy open")
	}
	return open, nil
}

// EncodeBroadcast builds the publish-once carrier payload: the (game,
// chunk) address, then the tick frame's bytes verbatim — replication
// forwards them without re-encoding.
func EncodeBroadcast(game, chunk string, frame []byte) ([]byte, error) {
	b := make([]byte, 0, 4+len(game)+len(chunk)+len(frame))
	var err error
	if b, err = appendProxyString(b, game); err != nil {
		return nil, err
	}
	if b, err = appendProxyString(b, chunk); err != nil {
		return nil, err
	}
	return append(b, frame...), nil
}

func DecodeBroadcast(payload []byte) (game, chunk string, frame []byte, err error) {
	at := 0
	if game, err = takeProxyString(payload, &at); err != nil {
		return "", "", nil, err
	}
	if chunk, err = takeProxyString(payload, &at); err != nil {
		return "", "", nil, err
	}
	frame = payload[at:]
	if game == "" || chunk == "" || len(frame) == 0 {
		return "", "", nil, errors.New("bad proxy broadcast")
	}
	return game, chunk, frame, nil
}

// frameBody splits one framed codec message into kind and payload without
// copying (the codec framing: RFC 9000 varint length, then kind byte).
// ok=false means the bytes are not exactly one whole frame.
func frameBody(frame []byte) (kind byte, payload []byte, ok bool) {
	if len(frame) == 0 {
		return 0, nil, false
	}
	prefix := 1 << (frame[0] >> 6)
	if len(frame) < prefix {
		return 0, nil, false
	}
	n := uint64(frame[0] & 0x3f)
	for _, b := range frame[1:prefix] {
		n = n<<8 | uint64(b)
	}
	body := frame[prefix:]
	if n == 0 || uint64(len(body)) != n {
		return 0, nil, false
	}
	return body[0], body[1:], true
}

// tickSpan reads the dense seq span from a tick payload's fixed header,
// without walking the record run.
func tickSpan(p []byte) (first, last int64, ok bool) {
	if len(p) < 18 {
		return 0, 0, false
	}
	first = int64(binary.LittleEndian.Uint64(p[8:16]))
	count := binary.LittleEndian.Uint16(p[16:18])
	if count == 0 {
		return 0, 0, false
	}
	return first, first + int64(count) - 1, true
}

// Hooks observe connection lifecycle for the owner's metrics; nil
// callbacks are skipped.
type Hooks struct {
	ConnUp    func(addr string)
	ConnDown  func(addr string, err error)
	DialError func(addr string)
}

// Pool is the gateway's side of the transport: one shared connection per
// backend address, dialed on first use and redialed on the next Open
// after a failure. A reconnect herd folds into a single dial because the
// per-address lock serializes it.
type Pool struct {
	key   []byte
	hooks Hooks
	// PingEvery and PongWithin pace the liveness probe each connection
	// runs, and IdleAfter is how long a connection with zero attachments
	// is kept before it is closed (backends leave the directory; their
	// connections must not outlive them). The defaults suit production
	// and tests shrink them.
	PingEvery  time.Duration
	PongWithin time.Duration
	IdleAfter  time.Duration

	mu    sync.Mutex
	conns map[string]*poolEntry
}

type poolEntry struct {
	mu   sync.Mutex
	conn *muxConn
}

func NewPool(key []byte, hooks Hooks) *Pool {
	return &Pool{
		key: key, hooks: hooks,
		PingEvery: defaultPingEvery, PongWithin: defaultPongWithin, IdleAfter: defaultIdleAfter,
		conns: map[string]*poolEntry{},
	}
}

// forgetConn detaches a dead connection from its address entry so the
// next Open dials fresh; the entry itself is the per-address dial lock
// and stays.
func (p *Pool) forgetConn(m *muxConn) {
	p.mu.Lock()
	e := p.conns[m.addr]
	p.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.conn == m {
		e.conn = nil
	}
	e.mu.Unlock()
}

// Open adds a new attachment over addr's shared connection, dialing it if
// needed. A connection that died since its last use costs one redial, not
// an error surfaced to the attachment.
func (p *Pool) Open(ctx context.Context, addr string, open Open) (*Attachment, error) {
	body, err := encodeOpen(open)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	e := p.conns[addr]
	if e == nil {
		e = &poolEntry{}
		p.conns[addr] = e
	}
	p.mu.Unlock()

	for attempt := 0; ; attempt++ {
		// The entry lock covers only the dial decision (ctx bounds the
		// dial itself); the open write happens outside it so one slow
		// write can't head-of-line-block every other attachment's open.
		e.mu.Lock()
		mc := e.conn
		if mc == nil || mc.dead() {
			var err error
			mc, err = p.dial(ctx, addr)
			if err != nil {
				e.mu.Unlock()
				if p.hooks.DialError != nil {
					p.hooks.DialError(addr)
				}
				return nil, err
			}
			e.conn = mc
		}
		e.mu.Unlock()
		a, err := mc.openAttachment(open, body)
		if err == nil || attempt > 0 {
			return a, err
		}
	}
}

func (p *Pool) dial(ctx context.Context, addr string) (*muxConn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	mc := &muxConn{
		Conn: &Conn{c: c}, addr: addr, pool: p, hooks: p.hooks,
		pingEvery: p.PingEvery, pongWithin: p.PongWithin, idleAfter: p.IdleAfter,
		attachments: map[uint64]*Attachment{}, byChunk: map[chunkKey]map[uint64]*Attachment{},
		deadCh: make(chan struct{}),
	}
	if err := mc.writeHandshake(p.key); err != nil {
		c.Close()
		return nil, err
	}
	mc.lastPong.Store(time.Now().UnixNano())
	go mc.readLoop()
	go mc.pingLoop()
	if p.hooks.ConnUp != nil {
		p.hooks.ConnUp(addr)
	}
	return mc, nil
}

type chunkKey struct{ game, chunk string }

type muxConn struct {
	*Conn
	addr       string
	pool       *Pool
	hooks      Hooks
	pingEvery  time.Duration
	pongWithin time.Duration
	idleAfter  time.Duration
	lastPong   atomic.Int64

	mu          sync.Mutex
	attachments map[uint64]*Attachment
	byChunk     map[chunkKey]map[uint64]*Attachment
	nextSID     uint64
	idleSince   time.Time
	deadCh      chan struct{}
	deadOnce    sync.Once
}

func (m *muxConn) dead() bool {
	select {
	case <-m.deadCh:
		return true
	default:
		return false
	}
}

func (m *muxConn) openAttachment(open Open, openBody []byte) (*Attachment, error) {
	m.mu.Lock()
	if m.dead() {
		m.mu.Unlock()
		return nil, errors.New("proxy conn closed")
	}
	m.nextSID++
	a := &Attachment{
		conn: m, sid: m.nextSID, key: chunkKey{open.Game, open.Chunk},
		sinceSeq: open.SinceSeq, pos: -1,
		events: make(chan Event, 256), done: make(chan struct{}),
	}
	m.attachments[a.sid] = a
	set := m.byChunk[a.key]
	if set == nil {
		set = map[uint64]*Attachment{}
		m.byChunk[a.key] = set
	}
	set[a.sid] = a
	// Inside the registry lock so a concurrent kill's sweep can only
	// decrement after this increment.
	mAttachments.Inc()
	m.mu.Unlock()
	if err := m.WriteMessage(KindOpen, a.sid, openBody); err != nil {
		m.kill(err)
		return nil, err
	}
	return a, nil
}

func (m *muxConn) lookup(sid uint64) *Attachment {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attachments[sid]
}

func (m *muxConn) forget(a *Attachment) {
	m.mu.Lock()
	mAttachments.Dec()
	delete(m.attachments, a.sid)
	if set := m.byChunk[a.key]; set != nil {
		delete(set, a.sid)
		if len(set) == 0 {
			delete(m.byChunk, a.key)
		}
	}
	m.mu.Unlock()
}

// chunkAttachments snapshots the broadcast fan-in for one chunk; gating
// runs outside the lock because it can terminate an attachment.
func (m *muxConn) chunkAttachments(game, chunk string) []*Attachment {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.byChunk[chunkKey{game, chunk}]
	out := make([]*Attachment, 0, len(set))
	for _, a := range set {
		out = append(out, a)
	}
	return out
}

func (m *muxConn) readLoop() {
	for {
		msg, err := m.ReadMessage()
		if err != nil {
			m.kill(err)
			return
		}
		switch msg.Kind {
		case KindPong:
			now := time.Now()
			m.lastPong.Store(now.UnixNano())
			if d, ok := pingRTT(msg.SID, now); ok {
				mRTT.Observe(d.Seconds())
			}
		case KindStream, KindDatagram:
			a := m.lookup(msg.SID)
			if a == nil || !a.deliver(Event{Kind: msg.Kind, Payload: msg.Payload}) {
				continue
			}
			if msg.Kind == KindStream {
				a.spliceUnicast(msg.Payload)
			}
		case KindBroadcast:
			game, chunk, frame, err := DecodeBroadcast(msg.Payload)
			if err != nil {
				continue
			}
			for _, a := range m.chunkAttachments(game, chunk) {
				a.spliceBroadcast(frame)
			}
		case KindClose:
			if a := m.lookup(msg.SID); a != nil {
				a.terminate(string(msg.Payload), true, false)
			}
		}
	}
}

func (m *muxConn) pingLoop() {
	t := time.NewTicker(m.pingEvery)
	defer t.Stop()
	for {
		select {
		case <-m.deadCh:
			return
		case <-t.C:
			if time.Since(time.Unix(0, m.lastPong.Load())) > m.pongWithin {
				m.kill(errors.New("pong timeout"))
				return
			}
			m.mu.Lock()
			switch {
			case len(m.attachments) > 0:
				m.idleSince = time.Time{}
			case m.idleSince.IsZero():
				m.idleSince = time.Now()
			}
			idle := !m.idleSince.IsZero() && time.Since(m.idleSince) > m.idleAfter
			m.mu.Unlock()
			if idle {
				m.kill(ErrIdle)
				return
			}
			if err := m.WriteMessage(KindPing, uint64(time.Now().UnixNano()), nil); err != nil {
				m.kill(err)
				return
			}
		}
	}
}

func (m *muxConn) kill(err error) {
	m.deadOnce.Do(func() {
		close(m.deadCh)
		m.Close()
		m.mu.Lock()
		attachments := make([]*Attachment, 0, len(m.attachments))
		for _, a := range m.attachments {
			attachments = append(attachments, a)
		}
		m.mu.Unlock()
		for _, a := range attachments {
			a.terminate("chunk unavailable", false, false)
		}
		// A goroutine because kill can run under a caller already holding
		// the pool entry's lock (a failed open).
		go m.pool.forgetConn(m)
		if m.hooks.ConnDown != nil {
			m.hooks.ConnDown(m.addr, err)
		}
	})
}

// Event is one message delivered to an attachment: a stream frame or a
// datagram from the chunk.
type Event struct {
	Kind    byte
	Payload []byte
}

// spliceMaxFrames/Bytes bound the per-attachment broadcast buffer: a gap
// the unicast lane hasn't filled within this much fan-out means the
// attachment's catch-up is stuck, and closing it hands recovery to the
// client's redial.
const (
	spliceMaxFrames = 256
	spliceMaxBytes  = 1 << 20
)

type pendingFrame struct {
	first, last int64
	frame       []byte
}

// Attachment is the gateway's handle on one multiplexed chunk session —
// its lifetime is independent of the connection carrying it.
type Attachment struct {
	conn *muxConn
	sid  uint64
	key  chunkKey

	events chan Event
	done   chan struct{}
	once   sync.Once
	// reason and fromChunk are written before done closes and read only
	// after it, so the channel close orders them.
	reason    string
	fromChunk bool

	// Splice state under spliceMu: the conn's readLoop runs the gate,
	// while terminate — reachable from any goroutine — settles the
	// buffer's gauge accounting. pos is the last seq handed to the relay
	// (-1 until the unicast lane anchors it); pending buffers broadcast
	// frames from beyond the gap the unicast catch-up is still filling;
	// sinceSeq is the resume position the open stated, which tells the
	// no-catch-up welcome apart.
	spliceMu     sync.Mutex
	spliceDone   bool
	sinceSeq     int64
	pos          int64
	pending      []pendingFrame
	pendingBytes int
}

func (a *Attachment) Events() <-chan Event  { return a.events }
func (a *Attachment) Done() <-chan struct{} { return a.done }

// deliver hands one event to the attachment's relay. An attachment that
// can't drain chunk fan-out is closed rather than allowed to stall every
// other attachment's demux.
func (a *Attachment) deliver(ev Event) bool {
	if a.enqueue(ev) {
		return true
	}
	a.terminate("relay backlog", false, true)
	return false
}

// enqueue is the non-closing half of deliver, for callers holding
// spliceMu (terminate needs that lock to settle the splice buffer).
func (a *Attachment) enqueue(ev Event) bool {
	select {
	case a.events <- ev:
		return true
	default:
		return false
	}
}

// spliceUnicast advances the splice position past a relayed unicast
// frame. A welcome whose Seq equals the stated resume position means no
// catch-up frames will follow, so it anchors pos itself; otherwise the
// catch-up ends exactly at the welcome's Seq and anchors there. Catch-up
// ticks advance monotonically (they ride behind the welcome that already
// names their endpoint). A snapshot is a restore point and resets pos
// outright — whatever the client saw beyond it is discarded by the
// restore, so stale buffered broadcasts drop with it.
func (a *Attachment) spliceUnicast(frame []byte) {
	kind, p, ok := frameBody(frame)
	if !ok {
		return
	}
	a.spliceMu.Lock()
	if a.spliceDone {
		a.spliceMu.Unlock()
		return
	}
	moved := false
	switch kind {
	case codec.KindWelcome:
		if w, err := codec.DecodeWelcome(p); err == nil && a.sinceSeq > 0 && w.Seq == a.sinceSeq {
			a.pos, moved = w.Seq, true
		}
	case codec.KindSnapshot:
		if s, err := codec.DecodeSnapshot(p); err == nil {
			a.pos, moved = s.Seq, true
		}
	case codec.KindTick:
		if _, last, ok := tickSpan(p); ok && last > a.pos {
			a.pos, moved = last, true
		}
	}
	var reason string
	if moved {
		reason = a.flushPending()
	}
	a.spliceMu.Unlock()
	if reason != "" {
		a.terminate(reason, false, true)
	}
}

// spliceBroadcast applies the seq gate to one broadcast tick frame.
// Broadcast and unicast are written by different backend goroutines, so
// their interleaving on the shared connection is not deterministic; the
// gate restores exactly-once-in-order delivery against the dense
// per-chunk seq line, which is what keeps the client-observable stream
// contract intact.
func (a *Attachment) spliceBroadcast(frame []byte) {
	kind, p, ok := frameBody(frame)
	if !ok || kind != codec.KindTick {
		return
	}
	first, last, ok := tickSpan(p)
	if !ok {
		return
	}
	var reason string
	a.spliceMu.Lock()
	switch {
	case a.spliceDone:
	case last <= a.pos:
		// Already delivered through the unicast lane.
		mBroadcasts.WithLabelValues("deduped").Inc()
	case a.pos >= 0 && first == a.pos+1:
		if !a.enqueue(Event{Kind: KindStream, Payload: frame}) {
			reason = "relay backlog"
			break
		}
		mBroadcasts.WithLabelValues("delivered").Inc()
		a.pos = last
		reason = a.flushPending()
	default:
		if len(a.pending) >= spliceMaxFrames || a.pendingBytes+len(frame) > spliceMaxBytes {
			mBroadcasts.WithLabelValues("overflow").Inc()
			reason = "splice backlog"
			break
		}
		a.pending = append(a.pending, pendingFrame{first: first, last: last, frame: frame})
		a.pendingBytes += len(frame)
		mBroadcasts.WithLabelValues("buffered").Inc()
		mSpliceBufFrames.Inc()
	}
	a.spliceMu.Unlock()
	if reason != "" {
		a.terminate(reason, false, true)
	}
}

// flushPending drains the buffer after pos moved, under spliceMu: frames
// the unicast lane already covered drop, frames the gap's closure exposed
// deliver in order. Broadcasts arrive in seq order per chunk, so the
// buffer is sorted by construction and one front-to-back pass suffices. A
// non-empty reason tells the caller to terminate once the lock drops.
func (a *Attachment) flushPending() (reason string) {
	for len(a.pending) > 0 {
		f := a.pending[0]
		if f.last > a.pos && f.first != a.pos+1 {
			return ""
		}
		a.pending = a.pending[1:]
		a.pendingBytes -= len(f.frame)
		mSpliceBufFrames.Dec()
		if f.last <= a.pos {
			mBroadcasts.WithLabelValues("deduped").Inc()
			continue
		}
		if !a.enqueue(Event{Kind: KindStream, Payload: f.frame}) {
			return "relay backlog"
		}
		mBroadcasts.WithLabelValues("delivered").Inc()
		a.pos = f.last
	}
	a.pending = nil
	return ""
}

// CloseReason reports why the attachment ended and whether the chunk said
// so; a chunk-stated reason is meant for the client, anything else is the
// transport's own failure. Valid once Done is closed.
func (a *Attachment) CloseReason() (string, bool) { return a.reason, a.fromChunk }

func (a *Attachment) SendStream(frame []byte) error { return a.send(KindStream, frame) }
func (a *Attachment) SendDatagram(b []byte) error   { return a.send(KindDatagram, b) }

func (a *Attachment) send(kind byte, b []byte) error {
	select {
	case <-a.done:
		return errAttachmentClosed
	default:
	}
	// A write error may have left a partial frame on the wire; the framing
	// has no resync marker, so the connection is unusable for everyone.
	if err := a.conn.WriteMessage(kind, a.sid, b); err != nil {
		a.conn.kill(err)
		return err
	}
	return nil
}

// Close ends the attachment and tells the chunk; the shared connection
// lives on for its other attachments.
func (a *Attachment) Close(reason string) { a.terminate(reason, false, true) }

func (a *Attachment) terminate(reason string, fromChunk, notifyChunk bool) {
	a.once.Do(func() {
		a.reason, a.fromChunk = reason, fromChunk
		a.spliceMu.Lock()
		a.spliceDone = true
		mSpliceBufFrames.Sub(float64(len(a.pending)))
		a.pending, a.pendingBytes = nil, 0
		a.spliceMu.Unlock()
		a.conn.forget(a)
		if notifyChunk {
			// Asynchronous: terminate is called from relay and demux loops
			// that must never block on a network write.
			go func() {
				if err := a.conn.WriteClose(a.sid, reason); err != nil {
					a.conn.kill(err)
				}
			}()
		}
		close(a.done)
	})
}
