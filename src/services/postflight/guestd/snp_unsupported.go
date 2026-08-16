//go:build !linux

package guestd

import (
	"fmt"
	"runtime"
)

func snpDerivedKey() ([]byte, error) {
	return nil, fmt.Errorf("guestd: derive SNP workspace key on %s: %w", runtime.GOOS, ErrSNPUnavailable)
}
