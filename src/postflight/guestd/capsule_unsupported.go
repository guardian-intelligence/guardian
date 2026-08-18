//go:build !linux

package guestd

import (
	"fmt"
	"os/exec"
	"runtime"
)

func configureCapsuleCommand(_ *exec.Cmd) error {
	return fmt.Errorf("guestd: configure capsule namespaces on %s: %w", runtime.GOOS, ErrLinuxRuntime)
}

func (m *CapsuleManager) attachCgroup(_ *exec.Cmd) (func(), error) {
	return nil, fmt.Errorf("guestd: attach capsule cgroup on %s: %w", runtime.GOOS, ErrLinuxRuntime)
}

func RunCapsuleEnter(_ []string) error {
	return fmt.Errorf("guestd: enter capsule namespaces on %s: %w", runtime.GOOS, ErrLinuxRuntime)
}
