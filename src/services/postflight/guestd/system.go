package guestd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
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
	// MakeFilesystem creates the filesystem on a blank device.
	MakeFilesystem(ctx context.Context, device, filesystem string) error
	// IsMounted reports whether something is mounted at the mountpoint.
	IsMounted(mountpoint string) (bool, error)
	// Mount mounts the device.
	Mount(ctx context.Context, device, mountpoint, filesystem string, options []string) error
	// Unmount unmounts the mountpoint.
	Unmount(mountpoint string) error
	// Sync flushes dirty pages ahead of an unmount.
	Sync()
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

// LocateDevice implements System via the udev-published by-id link for the
// QEMU scsi-hd serial — never by probe order.
func (RealSystem) LocateDevice(_ context.Context, serial string) (string, error) {
	link := guestproto.DiskByIDPrefix + serial
	device, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", fmt.Errorf("guestd: locating serial %s: %w", serial, err)
	}
	return device, nil
}

// IsBlank implements System with blkid's low-level probe.
func (RealSystem) IsBlank(ctx context.Context, device string) (bool, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", device)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return strings.TrimSpace(stdout.String()) == "", nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		// blkid exit 2: probing found no signature — a blank device.
		return true, nil
	}
	return false, fmt.Errorf("guestd: blkid %s: %s: %w", device, strings.TrimSpace(stderr.String()), err)
}

// MakeFilesystem implements System. Only ext4 is provisioned in the image;
// refusing anything else keeps a bad assignment from splicing arbitrary
// mkfs argv.
func (RealSystem) MakeFilesystem(ctx context.Context, device, filesystem string) error {
	if filesystem != "ext4" {
		return fmt.Errorf("guestd: unsupported filesystem %q", filesystem)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", device)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("guestd: mkfs.ext4 %s: %s: %w", device, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// IsMounted implements System by scanning /proc/self/mounts.
func (RealSystem) IsMounted(mountpoint string) (bool, error) {
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false, fmt.Errorf("guestd: reading mounts: %w", err)
	}
	target := path.Clean(mountpoint)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if unescapeMountPath(fields[1]) == target {
			return true, nil
		}
	}
	return false, nil
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

// Mount implements System.
func (r RealSystem) Mount(_ context.Context, device, mountpoint, filesystem string, options []string) error {
	if err := r.makeMountpoint(mountpoint); err != nil {
		return err
	}
	flags, data := mountOptions(options)
	if err := unix.Mount(device, mountpoint, filesystem, flags, data); err != nil {
		return fmt.Errorf("guestd: mounting %s at %s: %w", device, mountpoint, err)
	}
	return nil
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

// mountOptions splits mount options into kernel flags and filesystem data.
func mountOptions(options []string) (uintptr, string) {
	var flags uintptr
	var data []string
	for _, option := range options {
		switch option {
		case "nodev":
			flags |= unix.MS_NODEV
		case "nosuid":
			flags |= unix.MS_NOSUID
		case "noexec":
			flags |= unix.MS_NOEXEC
		case "noatime":
			flags |= unix.MS_NOATIME
		case "ro":
			flags |= unix.MS_RDONLY
		default:
			data = append(data, option)
		}
	}
	return flags, strings.Join(data, ",")
}

// Unmount implements System.
func (RealSystem) Unmount(mountpoint string) error {
	if err := unix.Unmount(mountpoint, 0); err != nil {
		return fmt.Errorf("guestd: unmounting %s: %w", mountpoint, err)
	}
	return nil
}

// Sync implements System.
func (RealSystem) Sync() { unix.Sync() }

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

// Adopt implements System.
func (r RealSystem) Adopt(mountpoint string) error {
	uid, gid, err := r.ownership()
	if err != nil {
		return err
	}
	if err := os.Chown(mountpoint, uid, gid); err != nil {
		return fmt.Errorf("guestd: adopting %s: %w", mountpoint, err)
	}
	marker := filepath.Join(mountpoint, WorkspaceMarker)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		return fmt.Errorf("guestd: writing marker: %w", err)
	}
	if err := os.Chown(marker, uid, gid); err != nil {
		return fmt.Errorf("guestd: owning marker: %w", err)
	}
	return nil
}
