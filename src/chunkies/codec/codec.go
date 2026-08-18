// Package codec is the chunkies wire protocol, v5: length-prefixed binary
// frames on a subscription stream, fixed-layout datagrams beside it, and
// the authority's durable record formats (records.go). Every field is
// little-endian except the frame length, a QUIC variable-length integer
// (RFC 9000 §16), which is big-endian by that spec's definition.
//
// The protocol is game-blind. The one shared unit is the EventRecord —
// `intent u64 | elen u16 | SimEvent`, where a SimEvent is
// `kind u16 | actor u64 | payload` — and the same record bytes serve as
// the intent envelope a client sends, the elements of the tick batch the
// authority fans out, and the body of the write-ahead TickRecord. An
// accepted intent's bytes are never re-encoded between those contexts,
// which is only safe because every structurally valid record has exactly
// one byte representation: decoders here reject non-canonical input
// (missing fields, trailing bytes, length disagreement) rather than
// normalize it.
//
// Three implementations — this one, the Rust chunkies-codec crate, and the
// TS testkit mirror — are held to the shared golden vectors in
// spec/vectors.txt; the vectors are the spec, the implementations are not.
package codec

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
)

const Proto = 5

// Frame kinds. 1-15 travel client to server, 16 and up server to client.
// 17 was v4's per-event kind and stays retired: a tag never changes its
// layout, it dies with it.
const (
	KindHello  = 1
	KindIntent = 2
	KindResync = 3

	KindWelcome  = 16
	KindReject   = 18
	KindSnapshot = 19
	KindTick     = 20
)

// Datagram kinds. A datagram's boundary is its message boundary, so these
// carry no length prefix — only a leading kind byte and a fixed layout.
const (
	DgCheck   = 1
	DgVerdict = 2

	CheckLen   = 29
	VerdictLen = 42
)

// MaxFrameLen bounds one frame body (kind byte plus payload) in every
// implementation — spec/caps.txt is the source of truth and spec_test.go
// holds this constant to it. A deflated snapshot sits far below it;
// anything larger is a peer that has lost the framing, not a big message.
const MaxFrameLen = 128 * 1024

// DatagramMax is the codec's contract for the datagram class: fixed
// layouts here stay far below it, and any future sample-class datagram is
// bounded by it statically. 1200 is the QUIC minimum path MTU floor;
// measuring the real path budget is the transport's job, not the codec's.
const DatagramMax = 1200

// EventRecordHeader is the fixed prefix of an EventRecord: intent u64,
// elen u16, then elen bytes of SimEvent. SimEventHeader is the fixed
// prefix of a SimEvent: kind u16, actor u64, then the game payload.
const (
	EventRecordHeader = 10
	SimEventHeader    = 10
)

// SystemIntent is the reserved intent id for authority-minted records
// (framework and game system events). A client-authored intent must carry
// a nonzero id — it is the dedup and ack handle — so decoders here refuse
// zero on the intent frame while accepting it inside tick batches.
const SystemIntent = 0

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

func EncodeFrame(kind byte, payload []byte) []byte {
	b := startFrame(kind, len(payload))
	return append(b, payload...)
}

// Reader decodes the frame stream. Payloads are freshly allocated:
// intents outlive the read, staged for a tick boundary and journaled.
type Reader struct {
	r   *bufio.Reader
	raw []byte
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
	// One allocation holds the whole frame (canonical varint + body) so a
	// relay can forward Raw() without re-framing.
	prefix := VarintLen(n)
	buf := AppendVarint(make([]byte, 0, prefix+int(n)), n)
	body := buf[prefix : prefix+int(n)]
	if _, err := io.ReadFull(fr.r, body); err != nil {
		return 0, nil, err
	}
	fr.raw = buf[:prefix+int(n)]
	return body[0], body[1:], nil
}

// Raw returns the last frame Next decoded, framed and ready to forward
// verbatim (the length prefix is re-encoded canonically). The slice is
// only valid until the next call to Next.
func (fr *Reader) Raw() []byte {
	return fr.raw
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

// ---------- the EventRecord ----------

// EventRecord is the one unit the protocol shares across contexts: the
// intent envelope, the tick batch element, and the TickRecord element.
// SimEvent is the trailing `kind | actor | payload` slice — exactly the
// bytes a simulation's apply receives — so validation and application
// never re-frame what arrived.
type EventRecord struct {
	Intent uint64
	Kind   uint16
	Actor  uint64
	// Payload is the game's bytes, opaque to the codec.
	Payload []byte
	// SimEvent aliases the underlying record: kind, actor, payload.
	SimEvent []byte
}

// AppendEventRecord appends one record built from parts. Envelope-building
// callers (the client session, system-event stamping) use this; relays and
// the authority move already-encoded records instead.
func AppendEventRecord(b []byte, intent uint64, kind uint16, actor uint64, payload []byte) []byte {
	b = binary.LittleEndian.AppendUint64(b, intent)
	b = binary.LittleEndian.AppendUint16(b, uint16(SimEventHeader+len(payload)))
	b = binary.LittleEndian.AppendUint16(b, kind)
	b = binary.LittleEndian.AppendUint64(b, actor)
	return append(b, payload...)
}

// parseRecord reads one record from the front of b, returning it and the
// bytes consumed. Zero consumed means b does not begin with a whole,
// well-formed record.
func parseRecord(b []byte) (EventRecord, int) {
	if len(b) < EventRecordHeader {
		return EventRecord{}, 0
	}
	elen := int(binary.LittleEndian.Uint16(b[8:10]))
	total := EventRecordHeader + elen
	if elen < SimEventHeader || len(b) < total {
		return EventRecord{}, 0
	}
	ev := b[EventRecordHeader:total:total]
	return EventRecord{
		Intent:   binary.LittleEndian.Uint64(b[0:8]),
		Kind:     binary.LittleEndian.Uint16(ev[0:2]),
		Actor:    binary.LittleEndian.Uint64(ev[2:10]),
		Payload:  ev[SimEventHeader:],
		SimEvent: ev,
	}, total
}

// ParseRecords validates a run of exactly count records covering all of b,
// returning them decoded. This is the shared walk under tick frames and
// TickRecords: after it succeeds the run is known canonical.
func ParseRecords(b []byte, count int) ([]EventRecord, error) {
	out := make([]EventRecord, 0, count)
	for len(out) < count {
		rec, n := parseRecord(b)
		if n == 0 {
			return nil, ErrBadPayload
		}
		out = append(out, rec)
		b = b[n:]
	}
	if len(b) != 0 {
		return nil, ErrBadPayload
	}
	return out, nil
}

// ---------- client to server ----------

// Hello opens a subscription stream: one stream, one chunk, one ticket.
// Since* name the resume position — (lineage, seq) identifies a stream of
// events, and a lineage the client has never seen means its history is
// gone and it must restore from a snapshot.
type Hello struct {
	Proto        uint16
	SinceLineage uint32
	SinceSeq     int64
	SinceTick    uint64
	Ticket       string
}

func EncodeHello(f Hello) []byte {
	b := startFrame(KindHello, 24+len(f.Ticket))
	b = binary.LittleEndian.AppendUint16(b, f.Proto)
	b = binary.LittleEndian.AppendUint32(b, f.SinceLineage)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.SinceSeq))
	b = binary.LittleEndian.AppendUint64(b, f.SinceTick)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(f.Ticket)))
	return append(b, f.Ticket...)
}

func DecodeHello(p []byte) (Hello, error) {
	c := cursor{b: p}
	var f Hello
	f.Proto = c.u16()
	f.SinceLineage = c.u32()
	f.SinceSeq = c.i64()
	f.SinceTick = c.u64()
	f.Ticket = string(c.bytes(int(c.u16())))
	if !c.done() {
		return Hello{}, ErrBadPayload
	}
	return f, nil
}

// EncodeIntent frames one client-authored EventRecord.
func EncodeIntent(intent uint64, kind uint16, actor uint64, payload []byte) []byte {
	b := startFrame(KindIntent, EventRecordHeader+SimEventHeader+len(payload))
	return AppendEventRecord(b, intent, kind, actor, payload)
}

// DecodeIntent decodes an intent frame's payload: exactly one EventRecord
// with a nonzero intent id (zero is the authority's to mint).
func DecodeIntent(p []byte) (EventRecord, error) {
	rec, n := parseRecord(p)
	if n == 0 || n != len(p) || rec.Intent == SystemIntent {
		return EventRecord{}, ErrBadPayload
	}
	return rec, nil
}

// IntentActorOffset is where the actor u64 sits inside an intent frame's
// payload, for the gateway's game-blind binding check against bytes it
// forwards verbatim.
const IntentActorOffset = EventRecordHeader + 2

type Resync struct {
	Lineage uint32
	HaveSeq int64
}

func EncodeResync(f Resync) []byte {
	b := startFrame(KindResync, 12)
	b = binary.LittleEndian.AppendUint32(b, f.Lineage)
	return binary.LittleEndian.AppendUint64(b, uint64(f.HaveSeq))
}

func DecodeResync(p []byte) (Resync, error) {
	c := cursor{b: p}
	f := Resync{Lineage: c.u32(), HaveSeq: c.i64()}
	if !c.done() {
		return Resync{}, ErrBadPayload
	}
	return f, nil
}

// ---------- server to client ----------

// Welcome opens every subscription. Lineage/Generation locate the stream's
// history (a lineage change means "discard what you saw and restore");
// Sub is this subscription's datagram tag. Content is the chunk's content
// blob id; hosts render it as hex for the content fetch path. Hz is a
// read-out of the world's journaled rate, which only ever changes while
// the chunk is dark, so a connection sees exactly one.
type Welcome struct {
	Lineage    uint32
	Generation uint32
	Sub        uint32
	Epoch      uint32
	Seq        int64
	Tick       uint64
	Hz         uint32
	Role       uint8
	Content    uint64
	Chunk      string
}

func EncodeWelcome(f Welcome) []byte {
	// The name's length rides in a u8, so it clamps rather than wrapping:
	// a truncated name is a readable frame, a wrapped one is not.
	chunk := f.Chunk
	if len(chunk) > 255 {
		chunk = chunk[:255]
	}
	b := startFrame(KindWelcome, 46+len(chunk))
	b = binary.LittleEndian.AppendUint32(b, f.Lineage)
	b = binary.LittleEndian.AppendUint32(b, f.Generation)
	b = binary.LittleEndian.AppendUint32(b, f.Sub)
	b = binary.LittleEndian.AppendUint32(b, f.Epoch)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.Seq))
	b = binary.LittleEndian.AppendUint64(b, f.Tick)
	b = binary.LittleEndian.AppendUint32(b, f.Hz)
	b = append(b, f.Role)
	b = binary.LittleEndian.AppendUint64(b, f.Content)
	b = append(b, byte(len(chunk)))
	return append(b, chunk...)
}

func DecodeWelcome(p []byte) (Welcome, error) {
	c := cursor{b: p}
	var f Welcome
	f.Lineage = c.u32()
	f.Generation = c.u32()
	f.Sub = c.u32()
	f.Epoch = c.u32()
	f.Seq = c.i64()
	f.Tick = c.u64()
	f.Hz = c.u32()
	f.Role = c.u8()
	f.Content = c.u64()
	f.Chunk = string(c.bytes(int(c.u8())))
	if !c.done() {
		return Welcome{}, ErrBadPayload
	}
	return f, nil
}

// Tick is one authority tick's accepted records. Seqs are dense from
// FirstSeq in record order, so they never ride per record. Records holds
// the encoded run — for the authority these are the accepted intents'
// bytes verbatim — and Decoded the validated view of it.
type Tick struct {
	Tick     uint64
	FirstSeq int64
	Records  []byte
	Count    uint16
}

// EncodeTick frames a tick batch around an already-encoded record run.
func EncodeTick(tick uint64, firstSeq int64, count uint16, records []byte) []byte {
	b := startFrame(KindTick, 18+len(records))
	b = binary.LittleEndian.AppendUint64(b, tick)
	b = binary.LittleEndian.AppendUint64(b, uint64(firstSeq))
	b = binary.LittleEndian.AppendUint16(b, count)
	return append(b, records...)
}

// DecodeTick validates the batch header and the whole record run.
func DecodeTick(p []byte) (Tick, []EventRecord, error) {
	c := cursor{b: p}
	var f Tick
	f.Tick = c.u64()
	f.FirstSeq = c.i64()
	f.Count = c.u16()
	if c.bad {
		return Tick{}, nil, ErrBadPayload
	}
	f.Records = c.b
	recs, err := ParseRecords(c.b, int(f.Count))
	if err != nil {
		return Tick{}, nil, err
	}
	return f, recs, nil
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
	Lineage uint32
	Seq     int64
	Tick    uint64
	Epoch   uint32
	WH      uint64
	Content uint64
	Z       []byte
}

func EncodeSnapshot(f Snapshot) []byte {
	b := startFrame(KindSnapshot, 44+len(f.Z))
	b = binary.LittleEndian.AppendUint32(b, f.Lineage)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.Seq))
	b = binary.LittleEndian.AppendUint64(b, f.Tick)
	b = binary.LittleEndian.AppendUint32(b, f.Epoch)
	b = binary.LittleEndian.AppendUint64(b, f.WH)
	b = binary.LittleEndian.AppendUint64(b, f.Content)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(f.Z)))
	return append(b, f.Z...)
}

func DecodeSnapshot(p []byte) (Snapshot, error) {
	c := cursor{b: p}
	var f Snapshot
	f.Lineage = c.u32()
	f.Seq = c.i64()
	f.Tick = c.u64()
	f.Epoch = c.u32()
	f.WH = c.u64()
	f.Content = c.u64()
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

// Check and Verdict carry Sub, the subscription tag from the welcome,
// because datagrams are connection-scoped while streams are per-chunk.
type Check struct {
	Sub  uint32
	Tick uint64
	WH   uint64
	CTMS uint64
}

func EncodeCheck(d Check) []byte {
	b := make([]byte, 0, CheckLen)
	b = append(b, DgCheck)
	b = binary.LittleEndian.AppendUint32(b, d.Sub)
	b = binary.LittleEndian.AppendUint64(b, d.Tick)
	b = binary.LittleEndian.AppendUint64(b, d.WH)
	return binary.LittleEndian.AppendUint64(b, d.CTMS)
}

func DecodeCheck(b []byte) (Check, error) {
	if len(b) != CheckLen || b[0] != DgCheck {
		return Check{}, ErrBadDatagram
	}
	c := cursor{b: b[1:]}
	return Check{Sub: c.u32(), Tick: c.u64(), WH: c.u64(), CTMS: c.u64()}, nil
}

// Verdict answers a check. Lineage rides every verdict so a client learns
// of a rewind on the datagram lane immediately, not at the next stream
// frame. CW/PW hold the first four bytes of each module's sha256 — the
// bytes the 8-hex-char display form spells out, in that order.
type Verdict struct {
	Sub     uint32
	Lineage uint32
	Tick    uint64
	Now     uint64
	CTMS    uint64
	Flags   uint8
	CW      uint32
	PW      uint32
}

func EncodeVerdict(d Verdict) []byte {
	b := make([]byte, 0, VerdictLen)
	b = append(b, DgVerdict)
	b = binary.LittleEndian.AppendUint32(b, d.Sub)
	b = binary.LittleEndian.AppendUint32(b, d.Lineage)
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
		Sub: c.u32(), Lineage: c.u32(),
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

// ActorFor derives the protocol's actor id from an authenticated subject:
// FNV-1a 64 of its bytes. Part of the wire contract, not a game rule —
// the gateway checks every intent envelope against it, and a game's own
// name for the result (WUM's dog id) is an alias of this value.
func ActorFor(sub string) uint64 {
	h := uint64(0xCBF29CE484222325)
	for _, b := range []byte(sub) {
		h ^= uint64(b)
		h *= 0x00000100000001B3
	}
	return h
}

func RoleCode(role string) uint8 {
	if role == "player" {
		return RolePlayer
	}
	return RoleSpectator
}
