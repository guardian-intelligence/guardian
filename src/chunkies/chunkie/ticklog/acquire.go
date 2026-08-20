package ticklog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrHeld reports a live predecessor still holding the volume: a wedged
// pod awaiting reap, or a force-deleted pod whose process has not died.
// The caller retries; it never fences by force.
var ErrHeld = errors.New("ticklog: volume held by live predecessor")

// Guard is exclusive write authority over one activation's volume
// directory — the writer lock. flock(2) on <dir>/LOCK is the mutual
// exclusion: liveness-independent (the kernel releases it with the
// process, no lease timers) and sufficient on this topology, where the
// volume is node-local and the workload node-pinned. The generation
// counter in <dir>/GENERATION is minted durably before Acquire returns,
// because no segment or checkpoint may ever exist under an unminted
// generation — the reader's fencing of a dead predecessor's leftovers
// depends on generations being monotonic per volume.
type Guard struct {
	dir        string
	lock       *os.File
	generation uint32
}

// Acquire takes the writer lock and mints this activation's generation.
// A corrupt counter refuses loudly — never auto-zero: zeroing would let
// a fresh writer collide with a prior generation's segments.
func Acquire(dir string) (*Guard, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("ticklog: flock: %w", err)
	}

	genPath := filepath.Join(dir, "GENERATION")
	var prev uint64
	switch b, err := os.ReadFile(genPath); {
	case errors.Is(err, os.ErrNotExist):
		// A fresh volume; the first activation writes generation 1.
	case err != nil:
		lock.Close()
		return nil, err
	default:
		prev, err = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
		if err != nil {
			lock.Close()
			return nil, fmt.Errorf("ticklog: %w: unreadable generation counter %q", ErrCorruptCounter, b)
		}
	}
	if prev >= uint64(^uint32(0)) {
		lock.Close()
		return nil, fmt.Errorf("ticklog: generation counter exhausted")
	}
	next := uint32(prev + 1)

	// tmp+rename+fsync: a named counter is by definition durable, the
	// same invariant segments use.
	tmp := genPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		lock.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(f, "%d\n", next); err == nil {
		err = f.Sync()
	}
	if err != nil {
		f.Close()
		lock.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		lock.Close()
		return nil, err
	}
	if err := os.Rename(tmp, genPath); err != nil {
		lock.Close()
		return nil, err
	}
	if err := (osFS{}).SyncDir(dir); err != nil {
		lock.Close()
		return nil, err
	}
	return &Guard{dir: dir, lock: lock, generation: next}, nil
}

// ErrCorruptCounter reports a generation counter that no longer parses.
var ErrCorruptCounter = errors.New("corrupt generation counter")

func (g *Guard) Generation() uint32 { return g.generation }

// Release drops the writer lock. The generation stays spent: a
// re-acquire is a new activation and mints the next one.
func (g *Guard) Release() error { return g.lock.Close() }
