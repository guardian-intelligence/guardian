// The chunkie's durable record formats: the node-local write-ahead
// segment and the checkpoint manifest. These share the wire's EventRecord
// so a tick's accepted intent bytes land on disk verbatim; everything the
// authority knows and the client does not (world hash, identity, the
// dedup window) rides in the wrappers, never inside the shared record.
//
// One segment file interleaves every chunk a chunkie hosts: the header's
// chunk table names them, and each record carries its chunk's table index
// so one group commit covers the whole pod. Records are framed
// `rlen u32 | rtype u8 | chunk u16 | body | crc32c u32` where rlen counts
// rtype+chunk+body and the checksum covers the same. The frame fields are
// the host's; a tick record's body is exactly what the simulation side
// produces for a tick, so a future in-module tick runner can emit it
// verbatim. Readers distinguish a torn tail (the expected artifact of a
// crash mid-append: truncate and continue) from corruption (refuse the
// segment, recover from a checkpoint).
//
// Epoch appears per chunk in the segment header, never per record: module
// promotion is a checkpoint barrier (drain the log, verify a checkpoint
// under the new module pair, only then announce), so a segment can never
// span an epoch advance and replay always runs under exactly one pair.
package codec

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

var (
	// ErrEnd reports a clean end of records: zero bytes remain.
	ErrEnd = errors.New("no records remain")
	// ErrTornTail reports an incomplete final record: a declared length
	// with too few bytes behind it, or the zero-fill of a preallocated
	// file. Recovery truncates here and proceeds.
	ErrTornTail = errors.New("record is torn")
	// ErrCorrupt reports a record that is whole but wrong: checksum
	// mismatch, unknown type, or an impossible length. Recovery must not
	// replay past it.
	ErrCorrupt = errors.New("record is corrupt")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// MaxRecordLen bounds one segment record (rtype plus chunk plus body);
// caps.txt is the source of truth. A record is one tick's accepted events
// — far smaller in practice.
const MaxRecordLen = 1 << 20

// MaxSegmentChunks bounds a segment's chunk table; caps.txt is the source
// of truth. A chunk steps on one core, so the bound is far past any pod.
const MaxSegmentChunks = 4096

// MaxSegmentHeaderLen bounds an encoded SegmentHeader: the fixed prelude,
// the largest possible chunk table, and the checksum. A reader may hand
// DecodeSegmentHeader this much of a file and let it report the consumed
// length, keeping the layout in exactly one place.
const MaxSegmentHeaderLen = 16 + MaxSegmentChunks*(1+255+16) + 4

const segmentMagic = "CHKW"

// SegmentVersion is the current segment layout. There is no cross-version
// reader: no prod segment predates v2.
const SegmentVersion = 2

// SegmentChunk is one chunk's row in a segment's chunk table. A chunk's
// index in the table is the index its records carry. Lineage stamps the
// history the records extend (a chunk rewinds alone, so lineage is
// per-chunk); Epoch is the module era the whole segment ran under, a
// cross-check on the checkpoint-barrier rule; FirstTick is the first tick
// this segment may hold for the chunk, bounding replay against a fenced
// writer's leftovers.
type SegmentChunk struct {
	Name      string
	Lineage   uint32
	Epoch     uint32
	FirstTick uint64
}

// SegmentHeader opens a write-ahead segment file. Generation stamps every
// segment so a fenced-out chunkie's leftovers are recognizable; Ordinal
// orders segments within a generation without parsing their records.
type SegmentHeader struct {
	Version    uint16
	Generation uint32
	Ordinal    uint32
	Chunks     []SegmentChunk
}

func EncodeSegmentHeader(h SegmentHeader) []byte {
	b := []byte(segmentMagic)
	b = binary.LittleEndian.AppendUint16(b, h.Version)
	b = binary.LittleEndian.AppendUint32(b, h.Generation)
	b = binary.LittleEndian.AppendUint32(b, h.Ordinal)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(h.Chunks)))
	for _, ch := range h.Chunks {
		name := ch.Name
		if len(name) > 255 {
			name = name[:255]
		}
		b = append(b, byte(len(name)))
		b = append(b, name...)
		b = binary.LittleEndian.AppendUint32(b, ch.Lineage)
		b = binary.LittleEndian.AppendUint32(b, ch.Epoch)
		b = binary.LittleEndian.AppendUint64(b, ch.FirstTick)
	}
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

// DecodeSegmentHeader decodes the header at the front of b, returning it
// and the bytes consumed. The header carries its own checksum: a torn or
// flipped header is ErrCorrupt — recovery for the whole segment falls to
// checkpoints, since without the chunk table no record is attributable.
func DecodeSegmentHeader(b []byte) (SegmentHeader, int, error) {
	if len(b) < 16 || string(b[:4]) != segmentMagic {
		return SegmentHeader{}, 0, ErrCorrupt
	}
	c := cursor{b: b[4:]}
	h := SegmentHeader{
		Version:    c.u16(),
		Generation: c.u32(),
		Ordinal:    c.u32(),
	}
	n := int(c.u16())
	if c.bad || n > MaxSegmentChunks {
		return SegmentHeader{}, 0, ErrCorrupt
	}
	h.Chunks = make([]SegmentChunk, n)
	for i := range h.Chunks {
		h.Chunks[i] = SegmentChunk{
			Name:      string(c.bytes(int(c.u8()))),
			Lineage:   c.u32(),
			Epoch:     c.u32(),
			FirstTick: c.u64(),
		}
	}
	sum := c.u32()
	if c.bad {
		return SegmentHeader{}, 0, ErrCorrupt
	}
	consumed := len(b) - len(c.b)
	if crc32.Checksum(b[:consumed-4], castagnoli) != sum {
		return SegmentHeader{}, 0, ErrCorrupt
	}
	return h, consumed, nil
}

// Record types.
const (
	RecordTick      = 1
	RecordWatermark = 2
)

// Record is one decoded segment record. Chunk indexes the segment
// header's chunk table. For RecordTick, the tick batch fields and record
// run are set; for RecordWatermark only Tick is (the tick the idle chunk
// had reached at the group-commit boundary).
type Record struct {
	Type     uint8
	Chunk    uint16
	Tick     uint64
	FirstSeq int64
	Count    uint16
	// Records is the encoded EventRecord run, verbatim wire bytes.
	Records []byte
	WH      uint64
}

func appendRecord(b []byte, rtype uint8, chunk uint16, body func([]byte) []byte) []byte {
	at := len(b)
	b = append(b, 0, 0, 0, 0) // rlen backfilled below
	b = append(b, rtype)
	b = binary.LittleEndian.AppendUint16(b, chunk)
	b = body(b)
	inner := b[at+4:]
	binary.LittleEndian.PutUint32(b[at:at+4], uint32(len(inner)))
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(inner, castagnoli))
}

// AppendTickRecord appends one tick's batch for one chunk: the same
// record run the tick frame carries, wrapped with what recovery needs —
// the resulting world hash, verified against every replayed record. The
// body past the chunk index (tick through hash) is the simulation side's
// output for the tick, laid out so a future in-module tick runner can
// produce those bytes verbatim.
func AppendTickRecord(b []byte, chunk uint16, tick uint64, firstSeq int64, count uint16, records []byte, wh uint64) []byte {
	return appendRecord(b, RecordTick, chunk, func(b []byte) []byte {
		b = binary.LittleEndian.AppendUint64(b, tick)
		b = binary.LittleEndian.AppendUint64(b, uint64(firstSeq))
		b = binary.LittleEndian.AppendUint16(b, count)
		b = append(b, records...)
		return binary.LittleEndian.AppendUint64(b, wh)
	})
}

// AppendWatermark appends the tick an idle chunk had reached, bounding
// replay work without recording anything else.
func AppendWatermark(b []byte, chunk uint16, tick uint64) []byte {
	return appendRecord(b, RecordWatermark, chunk, func(b []byte) []byte {
		return binary.LittleEndian.AppendUint64(b, tick)
	})
}

// ReadRecord decodes the record at the front of b, returning it and the
// bytes consumed. Errors are ErrEnd, ErrTornTail, or ErrCorrupt — see
// their contracts above.
func ReadRecord(b []byte) (Record, int, error) {
	if len(b) == 0 {
		return Record{}, 0, ErrEnd
	}
	if len(b) < 4 {
		return Record{}, 0, ErrTornTail
	}
	rlen := binary.LittleEndian.Uint32(b[:4])
	if rlen == 0 {
		// The zero-fill of a preallocated file reads as a zero length:
		// the tail from here was never written.
		return Record{}, 0, ErrTornTail
	}
	if rlen > MaxRecordLen {
		return Record{}, 0, ErrCorrupt
	}
	total := 4 + int(rlen) + 4
	if len(b) < total {
		return Record{}, 0, ErrTornTail
	}
	inner := b[4 : 4+rlen]
	if crc32.Checksum(inner, castagnoli) != binary.LittleEndian.Uint32(b[4+rlen:total]) {
		return Record{}, 0, ErrCorrupt
	}
	if rlen < 3 {
		return Record{}, 0, ErrCorrupt
	}
	rec := Record{Type: inner[0], Chunk: binary.LittleEndian.Uint16(inner[1:3])}
	c := cursor{b: inner[3:]}
	switch rec.Type {
	case RecordTick:
		rec.Tick = c.u64()
		rec.FirstSeq = c.i64()
		rec.Count = c.u16()
		if c.bad || len(c.b) < 8 {
			return Record{}, 0, ErrCorrupt
		}
		rec.Records = c.bytes(len(c.b) - 8)
		rec.WH = c.u64()
		if _, err := ParseRecords(rec.Records, int(rec.Count)); err != nil {
			return Record{}, 0, ErrCorrupt
		}
	case RecordWatermark:
		rec.Tick = c.u64()
	default:
		return Record{}, 0, ErrCorrupt
	}
	if !c.done() {
		return Record{}, 0, ErrCorrupt
	}
	return rec, total, nil
}

// ---------- the checkpoint manifest ----------

const checkpointMagic = "CHKC"

// DedupEntry is one remembered (actor, intent) acceptance. The window
// rides the checkpoint because "a resend can never double-apply" must
// survive a restart, and it rides travel records for the same reason at
// chunk boundaries.
type DedupEntry struct {
	Actor  uint64
	Intent uint64
}

// Checkpoint is the complete recovery manifest: everything an authority
// needs to reopen a chunk from nothing but this blob and wall time. One
// byte format serves the local copy and the control-plane copy; stores
// index a few fields as columns but the blob is the record.
type Checkpoint struct {
	Version    uint16
	Game       string
	Chunk      string
	Lineage    uint32
	Generation uint32
	Seq        int64
	Tick       uint64
	Epoch      uint32
	WH         uint64
	Content    uint64
	// CW and PW are the full sha256 of the client and sim module pair
	// the world was running — recovery must resume with the same rules.
	CW    [32]byte
	PW    [32]byte
	Dedup []DedupEntry
	// State is the module's snapshot blob, raw deflate.
	State []byte
}

func EncodeCheckpoint(f Checkpoint) []byte {
	game, chunk := f.Game, f.Chunk
	if len(game) > 255 {
		game = game[:255]
	}
	if len(chunk) > 255 {
		chunk = chunk[:255]
	}
	b := []byte(checkpointMagic)
	b = binary.LittleEndian.AppendUint16(b, f.Version)
	b = append(b, byte(len(game)))
	b = append(b, game...)
	b = append(b, byte(len(chunk)))
	b = append(b, chunk...)
	b = binary.LittleEndian.AppendUint32(b, f.Lineage)
	b = binary.LittleEndian.AppendUint32(b, f.Generation)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.Seq))
	b = binary.LittleEndian.AppendUint64(b, f.Tick)
	b = binary.LittleEndian.AppendUint32(b, f.Epoch)
	b = binary.LittleEndian.AppendUint64(b, f.WH)
	b = binary.LittleEndian.AppendUint64(b, f.Content)
	b = append(b, f.CW[:]...)
	b = append(b, f.PW[:]...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(f.Dedup)))
	for _, d := range f.Dedup {
		b = binary.LittleEndian.AppendUint64(b, d.Actor)
		b = binary.LittleEndian.AppendUint64(b, d.Intent)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(f.State)))
	b = append(b, f.State...)
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

func DecodeCheckpoint(b []byte) (Checkpoint, error) {
	if len(b) < 8 || string(b[:4]) != checkpointMagic {
		return Checkpoint{}, ErrCorrupt
	}
	body, sum := b[:len(b)-4], b[len(b)-4:]
	if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(sum) {
		return Checkpoint{}, ErrCorrupt
	}
	c := cursor{b: body[4:]}
	var f Checkpoint
	f.Version = c.u16()
	f.Game = string(c.bytes(int(c.u8())))
	f.Chunk = string(c.bytes(int(c.u8())))
	f.Lineage = c.u32()
	f.Generation = c.u32()
	f.Seq = c.i64()
	f.Tick = c.u64()
	f.Epoch = c.u32()
	f.WH = c.u64()
	f.Content = c.u64()
	copy(f.CW[:], c.bytes(32))
	copy(f.PW[:], c.bytes(32))
	n := c.u32()
	if c.bad || uint64(n) > uint64(len(c.b))/16 {
		return Checkpoint{}, ErrCorrupt
	}
	f.Dedup = make([]DedupEntry, n)
	for i := range f.Dedup {
		f.Dedup[i] = DedupEntry{Actor: c.u64(), Intent: c.u64()}
	}
	z := c.u32()
	if c.bad || uint64(z) > uint64(len(c.b)) {
		return Checkpoint{}, ErrCorrupt
	}
	f.State = c.bytes(int(z))
	if !c.done() {
		return Checkpoint{}, ErrCorrupt
	}
	return f, nil
}
