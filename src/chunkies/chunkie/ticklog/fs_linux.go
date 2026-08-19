//go:build linux

package ticklog

import (
	"os"

	"golang.org/x/sys/unix"
)

// preallocate reserves the file's full extent up front so appends never
// change its size: fdatasync then flushes data blocks only, never an
// inode size update, which is what keeps its latency flat. The reserved
// region reads as zeros — the reader's torn-tail contract depends on it.
func preallocate(f *os.File, size int64) error {
	if err := unix.Fallocate(int(f.Fd()), 0, 0, size); err != nil {
		// Filesystems without fallocate (some overlayfs in dev) fall back
		// to a sparse extent; holes still read as zeros.
		return f.Truncate(size)
	}
	return nil
}

func fdatasync(f *os.File) error { return unix.Fdatasync(int(f.Fd())) }
