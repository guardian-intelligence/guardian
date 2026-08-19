package ticklog

import (
	"os"
	"path/filepath"
)

// fsi is the engine's whole I/O surface, injectable so the failure tests
// can stall writes, shear syncs, and poison files without a real disk.
type fsi interface {
	// Create opens a fresh segment file preallocated to size bytes. The
	// unwritten region must read as zeros — the torn-tail contract.
	Create(path string, size int64) (segfile, error)
	// Rename atomically moves a fully-synced file into place: a segment
	// name only ever appears with a durable header behind it, so a
	// crash mid-creation leaves a temp file the reader ignores, never a
	// half-headed segment it must guess about.
	Rename(oldpath, newpath string) error
	Remove(path string) error
	// SyncDir makes the directory's entries durable.
	SyncDir(dir string) error
}

type segfile interface {
	WriteAt(p []byte, off int64) (int, error)
	// Sync makes written data durable (fdatasync where the platform has
	// it). A failed Sync poisons the file permanently: dirty pages may
	// have been dropped, so a later clean Sync proves nothing about the
	// lost writes. The engine never writes again after one fails.
	Sync() error
	Close() error
}

type osFS struct{}

func (osFS) Create(path string, size int64) (segfile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := preallocate(f, size); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return osSegfile{f}, nil
}

func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func (osFS) Remove(path string) error { return os.Remove(path) }

func (osFS) SyncDir(dir string) error {
	d, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type osSegfile struct{ f *os.File }

func (s osSegfile) WriteAt(p []byte, off int64) (int, error) { return s.f.WriteAt(p, off) }
func (s osSegfile) Sync() error                              { return fdatasync(s.f) }
func (s osSegfile) Close() error                             { return s.f.Close() }
