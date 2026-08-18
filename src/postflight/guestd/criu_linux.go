//go:build linux

package guestd

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// RunRestorePrivateInCgroup gives CRIU a disposable copy of the mount table
// and atomically starts it in the capsule cgroup. Restored descendants inherit
// both boundaries, so a failed attempt has one externally provable kill set.
func RunRestorePrivateInCgroup(cgroupPath string) func(context.Context, string, ...string) (string, error) {
	return func(ctx context.Context, path string, args ...string) (string, error) {
		fd, err := unix.Open(cgroupPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return "", fmt.Errorf("opening restore cgroup: %w", err)
		}
		defer unix.Close(fd)
		commandArgs := []string{"--mount", "--propagation", "private", "--", path}
		commandArgs = append(commandArgs, args...)
		cmd := exec.CommandContext(ctx, "/usr/bin/unshare", commandArgs...)
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("private mount restore failed: %w", err)
		}
		return string(output), nil
	}
}
