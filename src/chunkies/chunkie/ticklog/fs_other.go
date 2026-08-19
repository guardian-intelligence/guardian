//go:build !linux

package ticklog

import "os"

func preallocate(f *os.File, size int64) error { return f.Truncate(size) }

func fdatasync(f *os.File) error { return f.Sync() }
