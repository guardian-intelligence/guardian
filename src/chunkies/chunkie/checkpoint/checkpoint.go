// Package checkpoint is the chunkie's periodic-snapshot store (design
// of record §4): every 15–20s each chunk's opaque sim state lands on
// the same node-local volume as the WAL, so recovery is newest-valid-
// checkpoint plus bounded replay, and the WAL stays trimmable. The
// store is deliberately module-blind — the manifest's module-pair pin
// is a verification, not a lookup, and proving a checkpoint (restore +
// world-hash compare) is the caller's verb.
package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// Ref identifies one checkpoint on disk, parseable from its filename
// alone: <lineage>-<seq>-<tick>-<generation>-<epoch>.ckpt under
// <root>/<chunk>/, fixed-width hex so a directory listing sorts.
// Proven mirrors the presence of a sidecar <name>.ok marker: the
// checkpoint has survived a full disk round-trip restore with its
// world hash intact (Force at a promotion barrier, or the ladder).
type Ref struct {
	Chunk      string
	Lineage    uint32
	Seq        int64
	Tick       uint64
	Generation uint32
	Epoch      uint32
	Proven     bool
}

// Newer orders refs by recovery preference: lineage first (a rewind
// starts a newer history), then seq.
func (r Ref) Newer(o Ref) bool {
	if r.Lineage != o.Lineage {
		return r.Lineage > o.Lineage
	}
	return r.Seq > o.Seq
}

func (r Ref) name() string {
	return fmt.Sprintf("%08x-%016x-%016x-%08x-%08x.ckpt", r.Lineage, uint64(r.Seq), r.Tick, r.Generation, r.Epoch)
}

func (r Ref) path(root string) string {
	return filepath.Join(root, r.Chunk, r.name())
}

func parseName(chunk, name string) (Ref, bool) {
	r := Ref{Chunk: chunk}
	var seq uint64
	n, err := fmt.Sscanf(name, "%08x-%016x-%016x-%08x-%08x.ckpt", &r.Lineage, &seq, &r.Tick, &r.Generation, &r.Epoch)
	if err != nil || n != 5 || name != (Ref{Lineage: r.Lineage, Seq: int64(seq), Tick: r.Tick, Generation: r.Generation, Epoch: r.Epoch}).name() {
		return Ref{}, false
	}
	r.Seq = int64(seq)
	return r, true
}

// Store persists recovery manifests. The node-local Dir is the only
// implementation; the interface is the ruled seam for a future
// off-node sink.
type Store interface {
	// Put writes one manifest durably: tmp+rename, a named file is by
	// definition complete (the manifest carries its own CRC).
	Put(ctx context.Context, m codec.Checkpoint) (Ref, error)
	// List returns a chunk's refs newest-first by (lineage, seq).
	List(chunk string) ([]Ref, error)
	// Chunks enumerates every chunk with at least one checkpoint.
	Chunks() ([]string, error)
	// Load reads and integrity-checks one checkpoint; a file whose
	// decoded identity disagrees with its name is corruption.
	Load(r Ref) (codec.Checkpoint, error)
	// MarkProven records that r survived a full restore + world-hash
	// compare — the epoch guard's trigger.
	MarkProven(r Ref) error
	Remove(r Ref) error
}

// ErrMismatch reports a checkpoint whose decoded identity disagrees
// with its filename — corruption, never compatibility.
var ErrMismatch = errors.New("checkpoint: name/manifest mismatch")

// Dir is the node-local store. Writes are optionally smeared:
// WriteBudget caps bytes/sec so a checkpoint burst queues behind the
// WAL's group commit as a trickle, not a spike.
type Dir struct {
	root   string
	budget int64
}

func NewDir(root string, writeBudget int64) (*Dir, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Dir{root: root, budget: writeBudget}, nil
}

func (d *Dir) Put(ctx context.Context, m codec.Checkpoint) (Ref, error) {
	if err := validName(m.Chunk); err != nil {
		return Ref{}, err
	}
	r := Ref{Chunk: m.Chunk, Lineage: m.Lineage, Seq: m.Seq, Tick: m.Tick, Generation: m.Generation, Epoch: m.Epoch}
	dir := filepath.Join(d.root, m.Chunk)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Ref{}, err
		}
		// The chunk directory itself must survive power loss, or the
		// chunk's first checkpoint vanishes despite Put returning.
		if err := syncDir(d.root); err != nil {
			return Ref{}, err
		}
	}
	sweepTmp(dir)
	b := codec.EncodeCheckpoint(m)
	tmp := r.path(d.root) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return Ref{}, err
	}
	err = d.write(ctx, f, b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, r.path(d.root))
	}
	if err != nil {
		os.Remove(tmp)
		return Ref{}, err
	}
	return r, syncDir(dir)
}

// sweepTmp clears crash leftovers: tmp names embed the seq and are
// never reused, so an orphan would otherwise persist forever.
func sweepTmp(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func validName(chunk string) error {
	if chunk == "" || chunk == "." || chunk == ".." ||
		strings.ContainsAny(chunk, "/\\") {
		return fmt.Errorf("checkpoint: invalid chunk name %q", chunk)
	}
	return nil
}

// write smears the body under the byte budget: 1MiB slices with
// proportional sleeps, so the device sees a trickle. ctx aborts a
// smear mid-way (the tmp file is removed by the caller).
func (d *Dir) write(ctx context.Context, f *os.File, b []byte) error {
	const slice = 1 << 20
	for len(b) > 0 {
		n := min(len(b), slice)
		if _, err := f.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
		if d.budget > 0 && len(b) > 0 {
			select {
			case <-time.After(time.Duration(int64(n) * int64(time.Second) / d.budget)):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func (d *Dir) List(chunk string) ([]Ref, error) {
	if err := validName(chunk); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(filepath.Join(d.root, chunk))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	proven := map[string]bool{}
	var refs []Ref
	for _, e := range ents {
		name := e.Name()
		if filepath.Ext(name) == ".ok" {
			proven[name[:len(name)-3]] = true
			continue
		}
		if r, ok := parseName(chunk, name); ok {
			refs = append(refs, r)
		}
	}
	for i := range refs {
		refs[i].Proven = proven[refs[i].name()]
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Newer(refs[j]) })
	return refs, nil
}

func (d *Dir) Chunks() ([]string, error) {
	ents, err := os.ReadDir(d.root)
	if err != nil {
		return nil, err
	}
	var chunks []string
	for _, e := range ents {
		if e.IsDir() {
			chunks = append(chunks, e.Name())
		}
	}
	return chunks, nil
}

func (d *Dir) Load(r Ref) (codec.Checkpoint, error) {
	b, err := os.ReadFile(r.path(d.root))
	if err != nil {
		return codec.Checkpoint{}, err
	}
	m, err := codec.DecodeCheckpoint(b)
	if err != nil {
		return codec.Checkpoint{}, err
	}
	if m.Chunk != r.Chunk || m.Lineage != r.Lineage || m.Seq != r.Seq ||
		m.Tick != r.Tick || m.Generation != r.Generation || m.Epoch != r.Epoch {
		return codec.Checkpoint{}, ErrMismatch
	}
	return m, nil
}

func (d *Dir) MarkProven(r Ref) error {
	ok := r.path(d.root) + ".ok"
	f, err := os.OpenFile(ok, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(ok))
}

func (d *Dir) Remove(r Ref) error {
	os.Remove(r.path(d.root) + ".ok")
	if err := os.Remove(r.path(d.root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Join(d.root, r.Chunk))
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
