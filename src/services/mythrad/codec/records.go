// The authority's durable record formats: the node-local write-ahead
// segment and the checkpoint manifest. These share the wire's EventRecord
// so a tick's accepted intent bytes land on disk verbatim; everything the
// authority knows and the client does not (epoch, world hash, identity,
// the dedup window) rides in the wrappers, never inside the shared record.
//
// Segment layout: a SegmentHeader, then records framed
// `rlen u32 | rtype u8 | body | crc32c u32` where rlen counts rtype+body
// and the checksum covers the same. Readers distinguish a torn tail (the
// expected artifact of a crash mid-append: truncate and continue) from
// corruption (refuse the segment, recover from a checkpoint).
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

// MaxRecordLen bounds one segment record (rtype plus body); caps.txt is
// the source of truth. A record is one tick's accepted events — far
// smaller in practice.
const MaxRecordLen = 1 << 20

const segmentMagic = "CHKW"

// SegmentHeaderLen is the fixed byte length of an encoded SegmentHeader.
const SegmentHeaderLen = 22

// SegmentHeader opens a write-ahead segment file. Lineage and Generation
// stamp every segment so a fenced-out authority's leftovers are
// recognizable, and FirstTick orders segments without parsing them.
type SegmentHeader struct {
	Version    uint16
	Lineage    uint32
	Generation uint32
	FirstTick  uint64
}

func EncodeSegmentHeader(h SegmentHeader) []byte {
	b := make([]byte, 0, SegmentHeaderLen)
	b = append(b, segmentMagic...)
	b = binary.LittleEndian.AppendUint16(b, h.Version)
	b = binary.LittleEndian.AppendUint32(b, h.Lineage)
	b = binary.LittleEndian.AppendUint32(b, h.Generation)
	return binary.LittleEndian.AppendUint64(b, h.FirstTick)
}

func DecodeSegmentHeader(b []byte) (SegmentHeader, error) {
	if len(b) < SegmentHeaderLen || string(b[:4]) != segmentMagic {
		return SegmentHeader{}, ErrCorrupt
	}
	c := cursor{b: b[4:SegmentHeaderLen]}
	return SegmentHeader{
		Version:    c.u16(),
		Lineage:    c.u32(),
		Generation: c.u32(),
		FirstTick:  c.u64(),
	}, nil
}

// Record types.
const (
	RecordTick      = 1
	RecordWatermark = 2
)

// Record is one decoded segment record. For RecordTick, the tick batch
// fields and record run are set; for RecordWatermark only Tick is (the
// tick the idle authority had reached at the group-commit boundary).
type Record struct {
	Type     uint8
	Tick     uint64
	FirstSeq int64
	Epoch    uint32
	Count    uint16
	// Records is the encoded EventRecord run, verbatim wire bytes.
	Records []byte
	WH      uint64
}

func appendRecord(b []byte, rtype uint8, body func([]byte) []byte) []byte {
	at := len(b)
	b = append(b, 0, 0, 0, 0) // rlen backfilled below
	b = append(b, rtype)
	b = body(b)
	inner := b[at+4:]
	binary.LittleEndian.PutUint32(b[at:at+4], uint32(len(inner)))
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(inner, castagnoli))
}

// AppendTickRecord appends one tick's batch: the same record run the tick
// frame carries, wrapped with what recovery needs — epoch and the
// resulting world hash, verified against every replayed record.
func AppendTickRecord(b []byte, tick uint64, firstSeq int64, epoch uint32, count uint16, records []byte, wh uint64) []byte {
	return appendRecord(b, RecordTick, func(b []byte) []byte {
		b = binary.LittleEndian.AppendUint64(b, tick)
		b = binary.LittleEndian.AppendUint64(b, uint64(firstSeq))
		b = binary.LittleEndian.AppendUint32(b, epoch)
		b = binary.LittleEndian.AppendUint16(b, count)
		b = append(b, records...)
		return binary.LittleEndian.AppendUint64(b, wh)
	})
}

// AppendWatermark appends the tick an idle authority had reached, bounding
// replay work without recording anything else.
func AppendWatermark(b []byte, tick uint64) []byte {
	return appendRecord(b, RecordWatermark, func(b []byte) []byte {
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
	rec := Record{Type: inner[0]}
	c := cursor{b: inner[1:]}
	switch rec.Type {
	case RecordTick:
		rec.Tick = c.u64()
		rec.FirstSeq = c.i64()
		rec.Epoch = c.u32()
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
	// CW and PW are the full sha256 of the client and chunk module pair
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
