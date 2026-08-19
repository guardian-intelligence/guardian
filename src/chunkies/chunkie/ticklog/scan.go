package ticklog

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

const (
	segSuffix = ".chkw"
	tmpSuffix = ".tmp"
)

func segmentPath(dir string, generation, ordinal uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%08x-%08x%s", generation, ordinal, segSuffix))
}

func parseSegmentName(name string) (generation, ordinal uint32, ok bool) {
	base, found := strings.CutSuffix(name, segSuffix)
	if !found || len(base) != 17 || base[8] != '-' {
		return 0, 0, false
	}
	var g, o uint64
	if _, err := fmt.Sscanf(base, "%08x-%08x", &g, &o); err != nil {
		return 0, 0, false
	}
	return uint32(g), uint32(o), true
}

// ChunkKey selects one chunk's records for replay: only records on the
// chunk's current lineage, after the restored checkpoint's frontier.
type ChunkKey struct {
	Name    string
	Lineage uint32
	// AfterTick/AfterSeq are the restored checkpoint's frontier; replay
	// delivers strictly newer records and verifies the seq line stays
	// dense from AfterSeq.
	AfterTick uint64
	AfterSeq  int64
}

// Tips is the replay frontier per chunk after a Scan.
type Tips map[string]Tip

type segref struct {
	path       string
	generation uint32
	ordinal    uint32
	mtime      time.Time
	header     codec.SegmentHeader
	headerLen  int
}

// Scan replays the log: every segment in (generation, ordinal) order,
// each record delivered to apply exactly once, in journal order, per
// chunk. It never holds a segment in memory.
//
// The contract recovery leans on:
//   - A torn tail (crash artifact) ends a segment silently; replay
//     continues with the next segment.
//   - A corrupt record or header stops the scan with ErrCorrupt and the
//     tips reached; the caller must not serve past them.
//   - A generation's records are consumed only up to the next
//     generation's first tick for that chunk, so a fenced-out writer's
//     final unacked appends can never override its successor's history.
//   - A seq discontinuity is ErrGap: a missing segment must refuse to
//     open, never replay around the hole.
//
// Records for chunks not listed, on other lineages, or at or before the
// key's frontier are skipped without apply.
func Scan(dir string, chunks []ChunkKey, apply func(chunk string, r codec.Record) error) (Tips, error) {
	keys := make(map[string]*ChunkKey, len(chunks))
	tips := make(Tips, len(chunks))
	for i := range chunks {
		k := &chunks[i]
		keys[k.Name] = k
		tips[k.Name] = Tip{Tick: k.AfterTick, Seq: k.AfterSeq}
	}
	segs, err := readSegmentHeaders(dir)
	if err != nil && !errors.Is(err, codec.ErrCorrupt) {
		return tips, err
	}
	headerErr := err

	// A chunk's records in generation G are valid only up to where the
	// next generation that hosts the chunk restarted it.
	caps := generationCaps(segs)

	lastSeq := make(map[string]int64, len(chunks))
	for name, k := range keys {
		lastSeq[name] = k.AfterSeq
	}
	for si, s := range segs {
		if err := scanSegment(s, keys, caps[si], tips, lastSeq, apply); err != nil {
			return tips, err
		}
	}
	// A corrupt header stops replay at the segment before it; report it
	// after replaying the intact prefix so the caller has honest tips.
	return tips, headerErr
}

// readSegmentHeaders lists and orders the log's segments and decodes each
// header. A segment whose header does not decode ends the listing with
// ErrCorrupt: segment names only appear once their header is durable (the
// create-rename protocol), so a bad one is disk damage, never a crash
// artifact.
func readSegmentHeaders(dir string) ([]segref, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var segs []segref
	for _, e := range entries {
		g, o, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		segs = append(segs, segref{
			path: filepath.Join(dir, e.Name()), generation: g, ordinal: o, mtime: info.ModTime(),
		})
	}
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].generation != segs[j].generation {
			return segs[i].generation < segs[j].generation
		}
		return segs[i].ordinal < segs[j].ordinal
	})
	for i := range segs {
		h, n, err := readHeader(segs[i].path)
		if err != nil {
			return segs[:i], fmt.Errorf("%s: header: %w", segs[i].path, err)
		}
		if h.Generation != segs[i].generation || h.Ordinal != segs[i].ordinal {
			return segs[:i], fmt.Errorf("%s: header names %08x-%08x: %w",
				segs[i].path, h.Generation, h.Ordinal, codec.ErrCorrupt)
		}
		segs[i].header, segs[i].headerLen = h, n
	}
	return segs, nil
}

// readHeader hands the codec a bounded prefix of the file and lets its
// decoder — the layout's single owner — report the consumed length.
func readHeader(path string) (codec.SegmentHeader, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return codec.SegmentHeader{}, 0, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, codec.MaxSegmentHeaderLen))
	if err != nil {
		return codec.SegmentHeader{}, 0, err
	}
	return codec.DecodeSegmentHeader(buf)
}

// generationCaps returns, per segment, the exclusive tick bound for each
// of its table indexes: the first tick of the next generation hosting the
// same chunk, or no bound.
func generationCaps(segs []segref) [][]uint64 {
	const noCap = ^uint64(0)
	caps := make([][]uint64, len(segs))
	for i, s := range segs {
		caps[i] = make([]uint64, len(s.header.Chunks))
		for ci, ch := range s.header.Chunks {
			caps[i][ci] = noCap
			for _, later := range segs[i+1:] {
				if later.generation == s.generation {
					continue
				}
				if bound, ok := firstTickOf(later.header, ch.Name); ok {
					caps[i][ci] = bound
					break
				}
			}
		}
	}
	return caps
}

func firstTickOf(h codec.SegmentHeader, name string) (uint64, bool) {
	for _, ch := range h.Chunks {
		if ch.Name == name {
			return ch.FirstTick, true
		}
	}
	return 0, false
}

func scanSegment(s segref, keys map[string]*ChunkKey, caps []uint64, tips Tips,
	lastSeq map[string]int64, apply func(chunk string, r codec.Record) error) error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	if _, err := r.Discard(s.headerLen); err != nil {
		return fmt.Errorf("%s: %w", s.path, codec.ErrCorrupt)
	}
	frame := make([]byte, 0, 4096)
	for {
		head, err := r.Peek(4)
		if err != nil {
			return nil // physical end of file: a clean tail
		}
		rlen := binary.LittleEndian.Uint32(head)
		if rlen == 0 {
			// Zero fill usually means the log ends here — unless real
			// data follows, which no in-order crash can produce.
			return tornOrCorrupt(s.path, r)
		}
		if rlen > codec.MaxRecordLen {
			// No legal record and no prefix-shear of one reads this way.
			return fmt.Errorf("%s: %w", s.path, codec.ErrCorrupt)
		}
		total := 4 + int(rlen) + 4
		if cap(frame) < total {
			frame = make([]byte, 0, total)
		}
		frame = frame[:total]
		if _, err := io.ReadFull(r, frame); err != nil {
			return nil // torn mid-record at physical EOF: tail ends
		}
		rec, n, err := codec.ReadRecord(frame)
		if err != nil || n != total {
			// A frame that does not parse is either the sheared last
			// write of a crash (nothing but zeros can follow it) or
			// damage in the middle of history.
			return tornOrCorrupt(s.path, r)
		}
		if int(rec.Chunk) >= len(s.header.Chunks) {
			return fmt.Errorf("%s: chunk index %d outside table: %w", s.path, rec.Chunk, codec.ErrCorrupt)
		}
		ch := s.header.Chunks[rec.Chunk]
		key, wanted := keys[ch.Name]
		if !wanted || key.Lineage != ch.Lineage || rec.Tick >= caps[rec.Chunk] || rec.Tick <= key.AfterTick {
			continue
		}
		tip := tips[ch.Name]
		if rec.Type == codec.RecordWatermark {
			// Watermarks may trail tick records (they ride a slower
			// cadence); only an advance counts.
			if rec.Tick > tip.Tick {
				tips[ch.Name] = Tip{Tick: rec.Tick, Seq: tip.Seq}
			}
			continue
		}
		if rec.Tick <= tip.Tick {
			return fmt.Errorf("%s: chunk %q tick %d not after %d: %w",
				s.path, ch.Name, rec.Tick, tip.Tick, codec.ErrCorrupt)
		}
		if want := lastSeq[ch.Name] + 1; rec.FirstSeq != want {
			return fmt.Errorf("chunk %q at tick %d: firstSeq %d, want %d: %w",
				ch.Name, rec.Tick, rec.FirstSeq, want, ErrGap)
		}
		if err := apply(ch.Name, rec); err != nil {
			return err
		}
		lastSeq[ch.Name] = rec.FirstSeq + int64(rec.Count) - 1
		tips[ch.Name] = Tip{Tick: rec.Tick, Seq: lastSeq[ch.Name]}
	}
}

// tornOrCorrupt classifies a frame-level failure: a torn tail (the crash
// artifact preallocation promises: a sheared final write with nothing but
// zeros behind it) ends the segment silently, while damage with real
// data beyond it is corruption and must stop replay loudly. Out-of-order
// page writeback of an unsynced tail can also read as the latter — rare,
// and refusing costs only a checkpoint restore, never acked data.
func tornOrCorrupt(path string, r *bufio.Reader) error {
	for {
		rest, err := r.Peek(1 << 12)
		for _, b := range rest {
			if b != 0 {
				return fmt.Errorf("%s: %w", path, codec.ErrCorrupt)
			}
		}
		if err != nil {
			return nil // zeros to physical EOF
		}
		if _, err := r.Discard(len(rest)); err != nil {
			return nil
		}
	}
}

// Trim removes segments no longer needed for recovery: a segment may go
// only when a verified checkpoint frontier covers every chunk in it and
// the retention floor has elapsed — never by age alone. Coverage is
// decided from headers: everything a segment holds for a chunk precedes
// the chunk's first tick in the next segment that hosts it, so a
// candidate whose successors' first ticks are all within covered is fully
// checkpointed. Trimming is oldest-first and stops at the first keeper,
// preserving a contiguous history.
func (l *Log) Trim(covered Tips, floor time.Duration) (removed int, err error) {
	segs, err := readSegmentHeaders(l.cfg.Dir)
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	active := l.segPath
	l.mu.Unlock()
	now := l.now()
	for i, s := range segs[:max(len(segs)-1, 0)] {
		if s.path == active || now.Sub(s.mtime) < floor {
			break
		}
		trimmable := true
		for _, ch := range s.header.Chunks {
			bound, ok := nextFirstTick(segs[i+1:], ch.Name)
			cov, have := covered[ch.Name]
			if !ok || !have || cov.Tick < bound-1 {
				trimmable = false
				break
			}
		}
		if !trimmable {
			break
		}
		if err := l.fs.Remove(s.path); err != nil {
			return removed, err
		}
		removed++
	}
	if removed > 0 {
		err = l.fs.SyncDir(l.cfg.Dir)
	}
	return removed, err
}

func nextFirstTick(later []segref, name string) (uint64, bool) {
	for _, s := range later {
		if t, ok := firstTickOf(s.header, name); ok {
			return t, true
		}
	}
	return 0, false
}
