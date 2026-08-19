package ticklog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// memfs is the fault-injection filesystem: it mirrors every segment
// operation in memory, tracks which bytes each Sync made durable, and can
// stall writes, fail syncs, and materialize crash images onto a real
// directory for Scan.
type memfs struct {
	mu    sync.Mutex
	files map[string]*memfile

	failSyncAfter int  // >0: the Nth sync from now returns errInjected
	syncs         int  // total syncs observed
	stallWrites   bool // WriteAt blocks until cleared
	stallCh       chan struct{}

	// ops records the operation sequence for the no-write-after-failed-
	// sync assertion.
	ops []string
}

type memfile struct {
	name    string
	data    []byte
	synced  []byte // deep copy at last successful Sync
	poisons bool   // a Sync on this file failed; later ops are recorded but must not happen
}

var errInjected = errors.New("injected I/O fault")

func newMemfs() *memfs {
	return &memfs{files: map[string]*memfile{}, stallCh: make(chan struct{})}
}

func (m *memfs) Create(path string, size int64) (segfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[path]; ok {
		return nil, fmt.Errorf("create %s: exists", path)
	}
	f := &memfile{name: path, data: make([]byte, size), synced: make([]byte, size)}
	m.files[path] = f
	m.ops = append(m.ops, "create "+path)
	return &memsegfile{fs: m, f: f}, nil
}

func (m *memfs) Rename(oldpath, newpath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[oldpath]
	if !ok {
		return fmt.Errorf("rename %s: missing", oldpath)
	}
	delete(m.files, oldpath)
	f.name = newpath
	m.files[newpath] = f
	m.ops = append(m.ops, "rename "+oldpath+" "+newpath)
	return nil
}

func (m *memfs) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[path]; !ok {
		return os.ErrNotExist
	}
	delete(m.files, path)
	m.ops = append(m.ops, "remove "+path)
	return nil
}

func (m *memfs) SyncDir(dir string) error { return nil }

func (m *memfs) setStall(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stallWrites == on {
		return
	}
	m.stallWrites = on
	if !on {
		close(m.stallCh)
		m.stallCh = make(chan struct{})
	}
}

// image writes each file's crash image into dir: the bytes the last
// successful Sync covered, plus (optionally) everything written since —
// the two endpoints of what a real crash can leave, since unsynced pages
// may or may not have been written back.
func (m *memfs) image(dir string, includeUnsynced bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for path, f := range m.files {
		src := f.synced
		if includeUnsynced {
			src = f.data
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(path)), src, 0o600); err != nil {
			return err
		}
	}
	return nil
}

type memsegfile struct {
	fs *memfs
	f  *memfile
}

func (s *memsegfile) WriteAt(p []byte, off int64) (int, error) {
	for {
		s.fs.mu.Lock()
		if !s.fs.stallWrites {
			break
		}
		ch := s.fs.stallCh
		s.fs.mu.Unlock()
		<-ch
	}
	defer s.fs.mu.Unlock()
	s.fs.ops = append(s.fs.ops, fmt.Sprintf("write %s %d+%d", s.f.name, off, len(p)))
	if int64(len(s.f.data)) < off+int64(len(p)) {
		grown := make([]byte, off+int64(len(p)))
		copy(grown, s.f.data)
		s.f.data = grown
	}
	copy(s.f.data[off:], p)
	return len(p), nil
}

func (s *memsegfile) Sync() error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	s.fs.syncs++
	if s.fs.failSyncAfter > 0 {
		s.fs.failSyncAfter--
		if s.fs.failSyncAfter == 0 {
			s.f.poisons = true
			s.fs.ops = append(s.fs.ops, "sync-FAIL "+s.f.name)
			return errInjected
		}
	}
	s.fs.ops = append(s.fs.ops, "sync "+s.f.name)
	s.f.synced = append([]byte(nil), s.f.data...)
	return nil
}

func (s *memsegfile) Close() error { return nil }
