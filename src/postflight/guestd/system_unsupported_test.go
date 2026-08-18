//go:build !linux

package guestd

import (
	"context"
	"errors"
	"testing"
)

func TestRealSystemFailsClosedWhenUnsupported(t *testing.T) {
	system := RealSystem{}
	checks := []func() error{
		func() error { _, err := system.LocateDevice(context.Background(), "serial"); return err },
		func() error { _, err := system.IsMounted("/workspace"); return err },
		func() error { return system.Mount(context.Background(), "/dev/fake", "/workspace", "ext4", nil) },
		func() error { return system.Sync() },
	}
	for index, check := range checks {
		if err := check(); !errors.Is(err, ErrLinuxRuntime) {
			t.Fatalf("check %d error = %v, want ErrLinuxRuntime", index, err)
		}
	}
}
