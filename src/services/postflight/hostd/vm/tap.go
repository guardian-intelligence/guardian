package vm

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// TapLifecycle owns the host interface associated with one VM. Up must be
// idempotent because a hostd restart can resume between interface creation
// and QEMU launch; Down must succeed when the interface is already absent.
type TapLifecycle interface {
	Up(context.Context, string) error
	Down(context.Context, string) error
}

// ExecTapLifecycle invokes the pinned, root-owned host networking program.
// Hostd calls it before and after QEMU, so QEMU's spawn-deny sandbox remains
// enabled for the VM's entire lifetime.
type ExecTapLifecycle struct {
	Program string
}

func (e ExecTapLifecycle) Up(ctx context.Context, name string) error {
	return e.run(ctx, "up", name)
}

func (e ExecTapLifecycle) Down(ctx context.Context, name string) error {
	return e.run(ctx, "down", name)
}

func (e ExecTapLifecycle) run(ctx context.Context, operation, name string) error {
	if e.Program == "" {
		return fmt.Errorf("vm: tap lifecycle program is empty")
	}
	output, err := exec.CommandContext(ctx, e.Program, operation, name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("vm: tap %s %s: %w: %s", name, operation, err, strings.TrimSpace(string(output)))
	}
	return nil
}
