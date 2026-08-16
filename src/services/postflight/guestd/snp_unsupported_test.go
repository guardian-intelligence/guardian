//go:build !linux

package guestd

import (
	"errors"
	"testing"
)

func TestSNPEncryptionFailsClosedWhenUnsupported(t *testing.T) {
	key, err := workspaceKey(EncryptionSNP)
	if key != nil || !errors.Is(err, ErrSNPUnavailable) {
		t.Fatalf("workspaceKey(EncryptionSNP) = (%x, %v), want nil and ErrSNPUnavailable", key, err)
	}
}
