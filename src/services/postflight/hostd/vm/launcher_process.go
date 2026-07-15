package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcessLauncher runs QEMU as a direct child in its own session. The VM
// outlives this process, but not a host reboot and not supervisor-style —
// it exists for the conformance suite and manual bring-up; production is
// PodLauncher, where the pod's lifetime is the independence guarantee.
type ProcessLauncher struct{}

func pidFilePath(stateDir string) string { return filepath.Join(stateDir, "launcher.pid") }

// Start implements Launcher.
func (ProcessLauncher) Start(_ context.Context, _ ID, stateDir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("vm: empty argv")
	}
	log, err := os.OpenFile(filepath.Join(stateDir, "qemu.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("vm: opening qemu log: %w", err)
	}
	defer log.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("vm: starting %s: %w", argv[0], err)
	}
	pid := cmd.Process.Pid
	// Reap on our own exit path; after a hostd restart the orphan reparents
	// to init and adoption finds it via the pid file.
	go func() { _ = cmd.Wait() }()
	if err := writeFileAtomic(pidFilePath(stateDir), []byte(strconv.Itoa(pid))); err != nil {
		return fmt.Errorf("vm: recording pid: %w", err)
	}
	return nil
}

// Alive implements Launcher. The recorded pid counts only while
// /proc/<pid>/cmdline still matches the launched argv, so a recycled pid is
// never mistaken for the VM.
func (ProcessLauncher) Alive(_ context.Context, _ ID, stateDir string, argv []string) (bool, error) {
	pid, err := readPidFile(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return cmdlineMatches(pid, argv), nil
}

// Kill implements Launcher.
func (ProcessLauncher) Kill(ctx context.Context, _ ID, stateDir string, argv []string) error {
	pid, err := readPidFile(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !cmdlineMatches(pid, argv) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("vm: killing pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for cmdlineMatches(pid, argv) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vm: pid %d still running after SIGKILL", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func readPidFile(stateDir string) (int, error) {
	raw, err := os.ReadFile(pidFilePath(stateDir))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("vm: parsing pid file: %w", err)
	}
	return pid, nil
}

func cmdlineMatches(pid int, argv []string) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	want := []byte(strings.Join(argv, "\x00") + "\x00")
	return bytes.Equal(raw, want)
}

// writeFileAtomic lands content under path via a same-directory rename (a
// reader never observes a torn file) and fsyncs the directory: state written
// before side effects must survive a host power loss, or recovery would find
// the side effects without their owner.
func writeFileAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
