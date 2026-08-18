//go:build !linux

package guestd

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestPrivilegedPlatformEntrypointsFailClosedWhenUnsupported(t *testing.T) {
	command := exec.Command("true")
	checks := []func() error{
		func() error { return configureCapsuleCommand(command) },
		func() error { _, err := (&CapsuleManager{}).attachCgroup(command); return err },
		func() error {
			return RunCapsuleEnter([]string{"guestd", capsuleEnterArgument, "/sbin/init", "/bin/sleep"})
		},
		func() error {
			_, err := RunRestorePrivateInCgroup("/sys/fs/cgroup/postflight")(context.Background(), "/usr/sbin/criu", "restore")
			return err
		},
	}
	for index, check := range checks {
		if err := check(); !errors.Is(err, ErrLinuxRuntime) {
			t.Fatalf("check %d error = %v, want ErrLinuxRuntime", index, err)
		}
	}
}
