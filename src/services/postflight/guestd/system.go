package guestd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// System is the privileged-operation seam: everything guestd does to the
// guest besides running the runner. RealSystem drives the actual machine;
// tests substitute a fake so convergence logic is exercised hermetically.
type System interface {
	// LocateDevice resolves a SCSI serial to its block device. A device
	// that has not appeared yet is an error; the convergence loop retries.
	LocateDevice(ctx context.Context, serial string) (string, error)
	// IsBlank reports whether a device carries no filesystem signature.
	IsBlank(ctx context.Context, device string) (bool, error)
	// IsLUKS reports whether the device carries a LUKS header.
	IsLUKS(ctx context.Context, device string) (bool, error)
	// Discard punches out the device's entire contents (BLKDISCARD), so a
	// sparse backing volume reads zeros afterwards.
	Discard(ctx context.Context, device string) error
	// FormatLUKS initializes a LUKS2 container on the device with key.
	FormatLUKS(ctx context.Context, device string, key []byte) error
	// OpenLUKS opens the device's LUKS container under name and returns the
	// mapper device; a container already open under name is a success.
	OpenLUKS(ctx context.Context, device, name string, key []byte) (string, error)
	// MakeFilesystem creates the filesystem on a blank device.
	MakeFilesystem(ctx context.Context, device, filesystem string) error
	// IsMounted reports whether something is mounted at the mountpoint.
	IsMounted(mountpoint string) (bool, error)
	// Mount mounts the device.
	Mount(ctx context.Context, device, mountpoint, filesystem string, options []string) error
	// MountOverlay mounts a durable upper directory over an immutable lower
	// directory. The lower bind keeps the image-provided files visible after
	// target is covered by the merged mount.
	MountOverlay(ctx context.Context, lower, lowerBind, upper, work, target string, options []string) error
	// Unmount unmounts the mountpoint.
	Unmount(mountpoint string) error
	// Sync flushes dirty pages ahead of an unmount.
	Sync() error
	// Adopt hands a converged mountpoint to the runner user and drops the
	// workspace marker the checkout action asserts on.
	Adopt(mountpoint string) error
}

// RealSystem is the production System.
type RealSystem struct {
	// RunnerUser owns converged workspaces; empty means "runner".
	RunnerUser string
}

var _ System = RealSystem{}

func (r RealSystem) runnerUser() string {
	if r.RunnerUser != "" {
		return r.RunnerUser
	}
	return "runner"
}

// unescapeMountPath decodes the octal escapes (\040 and friends) the kernel
// uses for special characters in /proc mount tables.
func unescapeMountPath(escaped string) string {
	if !strings.Contains(escaped, `\`) {
		return escaped
	}
	var builder strings.Builder
	for i := 0; i < len(escaped); i++ {
		if escaped[i] == '\\' && i+3 < len(escaped) {
			if value, err := strconv.ParseUint(escaped[i+1:i+4], 8, 8); err == nil {
				builder.WriteByte(byte(value))
				i += 3
				continue
			}
		}
		builder.WriteByte(escaped[i])
	}
	return builder.String()
}

// makeMountpoint creates the mountpoint path, handing every directory it
// creates to the runner user: guestd runs privileged, and a root-owned
// intermediate (the _work/<repo> layer above the workspace) would wall the
// runner off from its own pipeline tree.
func (r RealSystem) makeMountpoint(mountpoint string) error {
	var created []string
	for dir := mountpoint; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("guestd: probing %s: %w", dir, err)
		}
		created = append(created, dir)
	}
	if len(created) == 0 {
		return nil
	}
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return fmt.Errorf("guestd: creating %s: %w", mountpoint, err)
	}
	uid, gid, err := r.ownership()
	if err != nil {
		return err
	}
	for _, dir := range created {
		if err := os.Chown(dir, uid, gid); err != nil {
			return fmt.Errorf("guestd: owning %s: %w", dir, err)
		}
	}
	return nil
}

// ownership resolves the runner user's uid and gid.
func (r RealSystem) ownership() (int, int, error) {
	account, err := user.Lookup(r.runnerUser())
	if err != nil {
		return 0, 0, fmt.Errorf("guestd: looking up %s: %w", r.runnerUser(), err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("guestd: uid of %s: %w", r.runnerUser(), err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("guestd: gid of %s: %w", r.runnerUser(), err)
	}
	return uid, gid, nil
}
