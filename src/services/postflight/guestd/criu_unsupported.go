//go:build !linux

package guestd

import (
	"context"
	"fmt"
	"runtime"
)

func RunRestorePrivateInCgroup(_ string) func(context.Context, string, ...string) (string, error) {
	return func(_ context.Context, _ string, _ ...string) (string, error) {
		return "", fmt.Errorf("guestd: restore private mount namespace on %s: %w", runtime.GOOS, ErrLinuxRuntime)
	}
}
