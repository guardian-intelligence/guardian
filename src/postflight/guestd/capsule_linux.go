//go:build linux

package guestd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func configureCapsuleCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWNS, Setsid: true}
	return nil
}

func (m *CapsuleManager) attachCgroup(cmd *exec.Cmd) (func(), error) {
	if err := m.prepareCgroup(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(m.CgroupPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("guestd: opening capsule cgroup: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = fd
	return func() { _ = unix.Close(fd) }, nil
}

// RunCapsuleEnter gives the PID namespace a matching procfs, then replaces
// the Go runtime with the native init so no guestd runtime state is captured.
func RunCapsuleEnter(args []string) error {
	if !IsCapsuleEnter(args) {
		return errors.New("guestd: invalid capsule-enter invocation")
	}
	initPath, sleepPath := args[2], args[3]
	if !filepath.IsAbs(initPath) || !filepath.IsAbs(sleepPath) {
		return errors.New("guestd: capsule-enter paths must be absolute")
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("guestd: making capsule mounts private: %w", err)
	}
	// CRIU images must stay outside the captured tree, while a private tmpfs
	// keeps process-backed temporary mappings available in the next guest.
	for _, mountpoint := range []string{ProcessMountpoint, RunnerHomeBackingMountpoint, RunnerHomeLowerMountpoint, "/boot/efi", "/boot", "/tmp"} {
		if err := syscall.Unmount(mountpoint, syscall.MNT_DETACH); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("guestd: detaching capsule mount %s: %w", mountpoint, err)
		}
	}
	if err := syscall.Mount("tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("guestd: mounting capsule tmpfs: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("guestd: mounting capsule procfs: %w", err)
	}
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("guestd: opening capsule null device: %w", err)
	}
	defer null.Close()
	for descriptor := 0; descriptor <= 2; descriptor++ {
		if err := syscall.Dup2(int(null.Fd()), descriptor); err != nil {
			return fmt.Errorf("guestd: redirecting capsule descriptor %d: %w", descriptor, err)
		}
	}
	return syscall.Exec(initPath, []string{initPath, "-s", "--", sleepPath, "infinity"}, []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	})
}
