//go:build !linux

package guestd

import (
	"context"
	"fmt"
	"runtime"
)

func unsupportedSystem(operation string) error {
	return fmt.Errorf("guestd: %s on %s: %w", operation, runtime.GOOS, ErrLinuxRuntime)
}

func (RealSystem) LocateDevice(context.Context, string) (string, error) {
	return "", unsupportedSystem("locate block device")
}

func (RealSystem) IsBlank(context.Context, string) (bool, error) {
	return false, unsupportedSystem("probe block device")
}

func (RealSystem) IsLUKS(context.Context, string) (bool, error) {
	return false, unsupportedSystem("probe LUKS container")
}

func (RealSystem) Discard(context.Context, string) error {
	return unsupportedSystem("discard block device")
}

func (RealSystem) FormatLUKS(context.Context, string, []byte) error {
	return unsupportedSystem("format LUKS container")
}

func (RealSystem) OpenLUKS(context.Context, string, string, []byte) (string, error) {
	return "", unsupportedSystem("open LUKS container")
}

func (RealSystem) MakeFilesystem(context.Context, string, string) error {
	return unsupportedSystem("make filesystem")
}

func (RealSystem) IsMounted(string) (bool, error) {
	return false, unsupportedSystem("inspect Linux mount table")
}

func (RealSystem) Mount(context.Context, string, string, string, []string) error {
	return unsupportedSystem("mount filesystem")
}

func (RealSystem) MountOverlay(context.Context, string, string, string, string, string, []string) error {
	return unsupportedSystem("mount overlay filesystem")
}

func (RealSystem) Unmount(string) error {
	return unsupportedSystem("unmount filesystem")
}

func (RealSystem) Sync() error {
	return unsupportedSystem("sync filesystems")
}

func (RealSystem) Adopt(string) error {
	return unsupportedSystem("adopt workspace")
}
