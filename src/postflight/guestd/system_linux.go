//go:build linux

package guestd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/guestproto"
	"golang.org/x/sys/unix"
)

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
		return true, nil
	}
	return false, fmt.Errorf("guestd: blkid %s: %s: %w", device, strings.TrimSpace(stderr.String()), err)
}

// MakeFilesystem implements System. Only ext4 is provisioned in the image;
// refusing anything else keeps a bad assignment from splicing arbitrary argv.
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

func (RealSystem) IsLUKS(ctx context.Context, device string) (bool, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "cryptsetup", "isLuks", device)
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("guestd: cryptsetup isLuks %s: %s: %w", device, strings.TrimSpace(stderr.String()), err)
}

func (RealSystem) FormatLUKS(ctx context.Context, device string, key []byte) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "cryptsetup", "luksFormat",
		"--batch-mode", "--type", "luks2",
		"--pbkdf", "pbkdf2", "--pbkdf-force-iterations", "1000",
		"--key-file", "-", device)
	cmd.Stdin = bytes.NewReader(key)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("guestd: luksFormat %s: %s: %w", device, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func (RealSystem) OpenLUKS(ctx context.Context, device, name string, key []byte) (string, error) {
	mapper := "/dev/mapper/" + name
	if _, err := os.Stat(mapper); err == nil {
		return mapper, nil
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "cryptsetup", "open", "--key-file", "-", "--allow-discards", device, name)
	cmd.Stdin = bytes.NewReader(key)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("guestd: cryptsetup open %s: %s: %w", device, strings.TrimSpace(stderr.String()), err)
	}
	return mapper, nil
}

func (RealSystem) Discard(ctx context.Context, device string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "blkdiscard", "-f", device)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("guestd: blkdiscard %s: %s: %w", device, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func (RealSystem) IsMounted(mountpoint string) (bool, error) {
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false, fmt.Errorf("guestd: reading mounts: %w", err)
	}
	target := path.Clean(mountpoint)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && unescapeMountPath(fields[1]) == target {
			return true, nil
		}
	}
	return false, nil
}

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

func (r RealSystem) MountOverlay(_ context.Context, lower, lowerBind, upper, work, target string, options []string) error {
	fstype, mounted, err := mountedFilesystem(target)
	if err != nil {
		return err
	}
	if mounted && fstype == "overlay" {
		return nil
	}
	if err := os.MkdirAll(lowerBind, 0o755); err != nil {
		return fmt.Errorf("guestd: creating overlay lower bind %s: %w", lowerBind, err)
	}
	if lowerMounted, err := r.IsMounted(lowerBind); err != nil {
		return err
	} else if !lowerMounted {
		if err := unix.Mount(lower, lowerBind, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("guestd: binding overlay lower %s at %s: %w", lower, lowerBind, err)
		}
		if err := unix.Mount("", lowerBind, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID, ""); err != nil {
			return fmt.Errorf("guestd: making overlay lower read-only at %s: %w", lowerBind, err)
		}
	}
	for _, directory := range []string{upper, work} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("guestd: creating overlay directory %s: %w", directory, err)
		}
	}
	if err := r.makeMountpoint(target); err != nil {
		return err
	}
	flags, optionData := mountOptions(options)
	dataOptions := []string{"lowerdir=" + lowerBind, "upperdir=" + upper, "workdir=" + work}
	if optionData != "" {
		dataOptions = append(dataOptions, optionData)
	}
	data := strings.Join(dataOptions, ",")
	if err := unix.Mount("overlay", target, "overlay", flags, data); err != nil {
		return fmt.Errorf("guestd: mounting runner-home overlay at %s: %w", target, err)
	}
	return nil
}

func mountedFilesystem(mountpoint string) (string, bool, error) {
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return "", false, fmt.Errorf("guestd: reading mounts: %w", err)
	}
	target := path.Clean(mountpoint)
	lines := strings.Split(string(raw), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		fields := strings.Fields(lines[index])
		if len(fields) >= 3 && unescapeMountPath(fields[1]) == target {
			return fields[2], true, nil
		}
	}
	return "", false, nil
}

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

func (RealSystem) Unmount(mountpoint string) error {
	if err := unix.Unmount(mountpoint, 0); err != nil {
		return fmt.Errorf("guestd: unmounting %s: %w", mountpoint, err)
	}
	return nil
}

func (RealSystem) Sync() error {
	unix.Sync()
	return nil
}

func (r RealSystem) Adopt(mountpoint string) error {
	uid, gid, err := r.ownership()
	if err != nil {
		return err
	}
	if err := os.Chown(mountpoint, uid, gid); err != nil {
		return fmt.Errorf("guestd: adopting %s: %w", mountpoint, err)
	}
	if err := os.RemoveAll(filepath.Join(mountpoint, "lost+found")); err != nil {
		return fmt.Errorf("guestd: removing lost+found: %w", err)
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
