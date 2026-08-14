// Package wire is the Wake Up Mythra session protocol: length-prefixed
// binary frames on the one bidi stream, fixed-layout datagrams beside it.
// Every field is little-endian except the frame length, a QUIC
// variable-length integer (RFC 9000 §16), which is big-endian by that
// spec's definition. Three implementations — this one, the Rust session
// core, and the TS host — must produce byte-identical output, so
// wire_test.go pins golden vectors rather than only round-trips.
package wire

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
)

const Proto = 4

// Frame kinds. 1-15 travel client to server, 16 and up server to client.
const (
	KindHello  = 1
	KindIntent = 2
	KindResync = 3

	KindWelcome  = 16
	KindEvent    = 17
	KindReject   = 18
	KindSnapshot = 19
)

// Datagram kinds. A datagram's boundary is its message boundary, so these
// carry no length prefix — only a leading kind byte and a fixed layout.
const (
	DgCheck   = 1
	DgVerdict = 2

	CheckLen   = 25
	VerdictLen = 34
)

// MaxFrameLen bounds one frame body (kind byte plus payload). A deflated
// park snapshot sits far below it; anything larger is a peer that has lost
// the framing, not a big message.
const MaxFrameLen = 128 * 1024

// Verdict flags: Known says the tick was still in the authority's hash
// ring, OK answers the comparison and is meaningful only alongside Known.
const (
	VerdictKnown = 1 << 0
	VerdictOK    = 1 << 1
)

const (
	RoleSpectator = 0
	RolePlayer    = 1
)

var (
	ErrShortFrame    = errors.New("frame carries no kind byte")
	ErrFrameTooLarge = errors.New("frame exceeds the size cap")
	ErrBadPayload    = errors.New("frame payload does not match its layout")
	ErrBadDatagram   = errors.New("datagram does not match its layout")
)

// ---------- variable-length integers ----------

func VarintLen(v uint64) int {
	switch {
	case v <= 63:
		return 1
	case v <= 16383:
		return 2
	case v <= 1073741823:
		return 4
	default:
		return 8
	}
}

// AppendVarint writes v in the shortest of RFC 9000 §16's four forms: a
// two-bit length prefix followed by the value in network byte order.
// Encoding is always shortest-form so frames are byte-stable across
// implementations; decoding accepts any form.
func AppendVarint(b []byte, v uint64) []byte {
	switch VarintLen(v) {
	case 1:
		return append(b, byte(v))
	case 2:
		return append(b, byte(v>>8)|0x40, byte(v))
	case 4:
		return append(b, byte(v>>24)|0x80, byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(b, byte(v>>56)|0xc0, byte(v>>48), byte(v>>40), byte(v>>32),
			byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}

func ReadVarint(r *bufio.Reader) (uint64, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	v := uint64(first & 0x3f)
	for i := 1; i < 1<<(first>>6); i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// FrameSize is the on-wire byte count of a frame carrying payloadLen bytes
// of payload, for callers accounting for bandwidth.
func FrameSize(payloadLen int) int {
	body := payloadLen + 1
	return VarintLen(uint64(body)) + body
}

// ---------- framing ----------

// startFrame lays down the length prefix and kind for a body of exactly
// payloadLen+1 bytes, sizing the buffer so the whole frame is one
// allocation.
func startFrame(kind byte, payloadLen int) []byte {
	body := payloadLen + 1
	b := make([]byte, 0, VarintLen(uint64(body))+body)
	return append(AppendVarint(b, uint64(body)), kind)
}

// Reader decodes the frame stream. Payloads are freshly allocated:
// intents outlive the read, staged for a tick boundary and journaled.
type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 8*1024)}
}

// Next returns the next frame's kind and payload.
func (fr *Reader) Next() (byte, []byte, error) {
	n, err := ReadVarint(fr.r)
	if err != nil {
		return 0, nil, err
	}
	switch {
	case n == 0:
		return 0, nil, ErrShortFrame
	case n > MaxFrameLen:
		return 0, nil, ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(fr.r, body); err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}

// ---------- payload cursor ----------

var zeroField [8]byte

// cursor reads fixed-width fields, latching overrun so a decoder can lay
// its fields out in order and bounds-check once at the end.
type cursor struct {
	b   []byte
	bad bool
}

func (c *cursor) take(n int) []byte {
	if c.bad || len(c.b) < n {
		c.bad = true
		return zeroField[:n]
	}
	out := c.b[:n]
	c.b = c.b[n:]
	return out
}

func (c *cursor) u8() uint8   { return c.take(1)[0] }
func (c *cursor) u16() uint16 { return binary.LittleEndian.Uint16(c.take(2)) }
func (c *cursor) u32() uint32 { return binary.LittleEndian.Uint32(c.take(4)) }
func (c *cursor) u64() uint64 { return binary.LittleEndian.Uint64(c.take(8)) }
func (c *cursor) i64() int64  { return int64(c.u64()) }

func (c *cursor) bytes(n int) []byte {
	if c.bad || len(c.b) < n {
		c.bad = true
		return nil
	}
	out := c.b[:n:n]
	c.b = c.b[n:]
	return out
}

// done reports a clean decode: every field present and nothing trailing.
// Trailing bytes are an encoder disagreeing with this layout, which is
// exactly the failure the three implementations must not paper over.
func (c *cursor) done() bool { return !c.bad && len(c.b) == 0 }

// ---------- client to server ----------

type Hello struct {
	Proto     uint16
	SinceSeq  int64
	SinceTick uint64
	Ticket    string
}

func EncodeHello(f Hello) []byte {
	b := startFrame(KindHello, 20+len(f.Ticket))
	b = binary.LittleEndian.AppendUint16(b, f.Proto)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.SinceSeq))
	b = binary.LittleEndian.AppendUint64(b, f.SinceTick)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(f.Ticket)))
	return append(b, f.Ticket...)
}

func DecodeHello(p []byte) (Hello, error) {
	c := cursor{b: p}
	var f Hello
	f.Proto = c.u16()
	f.SinceSeq = c.i64()
	f.SinceTick = c.u64()
	f.Ticket = string(c.bytes(int(c.u16())))
	if !c.done() {
		return Hello{}, ErrBadPayload
	}
	return f, nil
}

type Intent struct {
	ID      uint64
	Kind    uint16
	Payload []byte
}

func EncodeIntent(f Intent) []byte {
	b := startFrame(KindIntent, 12+len(f.Payload))
	b = binary.LittleEndian.AppendUint64(b, f.ID)
	b = binary.LittleEndian.AppendUint16(b, f.Kind)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(f.Payload)))
	return append(b, f.Payload...)
}

func DecodeIntent(p []byte) (Intent, error) {
	c := cursor{b: p}
	var f Intent
	f.ID = c.u64()
	f.Kind = c.u16()
	f.Payload = c.bytes(int(c.u16()))
	if !c.done() {
		return Intent{}, ErrBadPayload
	}
	return f, nil
}

type Resync struct {
	HaveSeq int64
}

func EncodeResync(f Resync) []byte {
	return binary.LittleEndian.AppendUint64(startFrame(KindResync, 8), uint64(f.HaveSeq))
}

func DecodeResync(p []byte) (Resync, error) {
	c := cursor{b: p}
	f := Resync{HaveSeq: c.i64()}
	if !c.done() {
		return Resync{}, ErrBadPayload
	}
	return f, nil
}

// ---------- server to client ----------

// Welcome opens every session. Terrain is the blob id itself; hosts render
// it as hex for /terrain/<id>. Hz is a read-out of the world's journaled
// rate, which only ever changes while the park is dark, so a connection
// sees exactly one.
type Welcome struct {
	Epoch   uint32
	Seq     int64
	Tick    uint64
	Hz      uint32
	Role    uint8
	Terrain uint64
	Park    string
}

func EncodeWelcome(f Welcome) []byte {
	park := f.Park
	if len(park) > 255 {
		park = park[:255]
	}
	b := startFrame(KindWelcome, 34+len(park))
	b = binary.LittleEndian.AppendUint32(b, f.Epoch)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.Seq))
	b = binary.LittleEndian.AppendUint64(b, f.Tick)
	b = binary.LittleEndian.AppendUint32(b, f.Hz)
	b = append(b, f.Role)
	b = binary.LittleEndian.AppendUint64(b, f.Terrain)
	b = append(b, byte(len(park)))
	return append(b, park...)
}

func DecodeWelcome(p []byte) (Welcome, error) {
	c := cursor{b: p}
	var f Welcome
	f.Epoch = c.u32()
	f.Seq = c.i64()
	f.Tick = c.u64()
	f.Hz = c.u32()
	f.Role = c.u8()
	f.Terrain = c.u64()
	f.Park = string(c.bytes(int(c.u8())))
	if !c.done() {
		return Welcome{}, ErrBadPayload
	}
	return f, nil
}

// Event is a journal event as peers see it. The actor stays in the
// journal: authorship is a durable fact, not something every replica needs
// to apply the event. Consumers matching their own submissions do it by
// Intent, which must therefore be unique per sender.
type Event struct {
	Seq     int64
	Tick    uint64
	Kind    uint16
	Intent  uint64
	Payload []byte
}

func EncodeEvent(f Event) []byte {
	b := startFrame(KindEvent, 28+len(f.Payload))
	b = binary.LittleEndian.AppendUint64(b, uint64(f.Seq))
	b = binary.LittleEndian.AppendUint64(b, f.Tick)
	b = binary.LittleEndian.AppendUint16(b, f.Kind)
	b = binary.LittleEndian.AppendUint64(b, f.Intent)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(f.Payload)))
	return append(b, f.Payload...)
}

func DecodeEvent(p []byte) (Event, error) {
	c := cursor{b: p}
	var f Event
	f.Seq = c.i64()
	f.Tick = c.u64()
	f.Kind = c.u16()
	f.Intent = c.u64()
	f.Payload = c.bytes(int(c.u16()))
	if !c.done() {
		return Event{}, ErrBadPayload
	}
	return f, nil
}

type Reject struct {
	Intent uint64
	Reason uint32
}

func EncodeReject(f Reject) []byte {
	b := startFrame(KindReject, 12)
	b = binary.LittleEndian.AppendUint64(b, f.Intent)
	return binary.LittleEndian.AppendUint32(b, f.Reason)
}

func DecodeReject(p []byte) (Reject, error) {
	c := cursor{b: p}
	var f Reject
	f.Intent = c.u64()
	f.Reason = c.u32()
	if !c.done() {
		return Reject{}, ErrBadPayload
	}
	return f, nil
}

// Snapshot carries raw deflate of the module's state blob.
type Snapshot struct {
	Seq     int64
	Tick    uint64
	Epoch   uint32
	WH      uint64
	Terrain uint64
	Z       []byte
}

func EncodeSnapshot(f Snapshot) []byte {
	b := startFrame(KindSnapshot, 40+len(f.Z))
	b = binary.LittleEndian.AppendUint64(b, uint64(f.Seq))
	b = binary.LittleEndian.AppendUint64(b, f.Tick)
	b = binary.LittleEndian.AppendUint32(b, f.Epoch)
	b = binary.LittleEndian.AppendUint64(b, f.WH)
	b = binary.LittleEndian.AppendUint64(b, f.Terrain)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(f.Z)))
	return append(b, f.Z...)
}

func DecodeSnapshot(p []byte) (Snapshot, error) {
	c := cursor{b: p}
	var f Snapshot
	f.Seq = c.i64()
	f.Tick = c.u64()
	f.Epoch = c.u32()
	f.WH = c.u64()
	f.Terrain = c.u64()
	n := c.u32()
	if c.bad || uint64(n) > uint64(len(c.b)) {
		return Snapshot{}, ErrBadPayload
	}
	f.Z = c.bytes(int(n))
	if !c.done() {
		return Snapshot{}, ErrBadPayload
	}
	return f, nil
}

// ---------- datagrams ----------

type Check struct {
	Tick uint64
	WH   uint64
	CTMS uint64
}

func EncodeCheck(d Check) []byte {
	b := make([]byte, 0, CheckLen)
	b = append(b, DgCheck)
	b = binary.LittleEndian.AppendUint64(b, d.Tick)
	b = binary.LittleEndian.AppendUint64(b, d.WH)
	return binary.LittleEndian.AppendUint64(b, d.CTMS)
}

func DecodeCheck(b []byte) (Check, error) {
	if len(b) != CheckLen || b[0] != DgCheck {
		return Check{}, ErrBadDatagram
	}
	c := cursor{b: b[1:]}
	return Check{Tick: c.u64(), WH: c.u64(), CTMS: c.u64()}, nil
}

// Verdict answers a check and rides the current module hashes, so a page
// learns mid-session that its wasm is stale. CW/PW hold the first four
// bytes of each module's sha256 — the bytes the 8-hex-char display form
// spells out, in that order.
type Verdict struct {
	Tick  uint64
	Now   uint64
	CTMS  uint64
	Flags uint8
	CW    uint32
	PW    uint32
}

func EncodeVerdict(d Verdict) []byte {
	b := make([]byte, 0, VerdictLen)
	b = append(b, DgVerdict)
	b = binary.LittleEndian.AppendUint64(b, d.Tick)
	b = binary.LittleEndian.AppendUint64(b, d.Now)
	b = binary.LittleEndian.AppendUint64(b, d.CTMS)
	b = append(b, d.Flags)
	b = binary.LittleEndian.AppendUint32(b, d.CW)
	return binary.LittleEndian.AppendUint32(b, d.PW)
}

func DecodeVerdict(b []byte) (Verdict, error) {
	if len(b) != VerdictLen || b[0] != DgVerdict {
		return Verdict{}, ErrBadDatagram
	}
	c := cursor{b: b[1:]}
	return Verdict{
		Tick: c.u64(), Now: c.u64(), CTMS: c.u64(),
		Flags: c.u8(), CW: c.u32(), PW: c.u32(),
	}, nil
}

// ModuleWord packs a distribution hash for a verdict. The display form is
// hex of the sha256's first four bytes, so decoding it and reading those
// bytes little-endian puts them back on the wire in their original order.
func ModuleWord(hash string) uint32 {
	var b [4]byte
	if _, err := hex.Decode(b[:], []byte(hash)); err != nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b[:])
}

// HexWord is ModuleWord's inverse: the 8-character display form.
func HexWord(word uint32) string {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], word)
	return hex.EncodeToString(b[:])
}

func RoleCode(role string) uint8 {
	if role == "player" {
		return RolePlayer
	}
	return RoleSpectator
}
