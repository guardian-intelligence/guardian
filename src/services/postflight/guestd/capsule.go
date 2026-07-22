package guestd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	capsuleEnterArgument = "capsule-enter"
)

// CapsuleManager owns one checkpointable PID namespace. Its init never reads
// a runner credential. Runner.Worker enters the namespace only after hostd
// authorizes the locally observed assignment; when it exits, namespace init
// adopts surviving build daemons and forms one generic CRIU tree.
type CapsuleManager struct {
	BinaryPath string
	InitPath   string
	SleepPath  string
	RunnerRoot string
	// CgroupPath confines every cold or restored capsule. A failed restore may
	// continue cold only after the killed, empty cgroup is replaced with a new
	// cgroup identity.
	CgroupPath string

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	rootPID     int
	rootReady   bool
	command     *exec.Cmd
}

func (m *CapsuleManager) validate() error {
	switch {
	case m == nil:
		return errors.New("guestd: capsule manager is nil")
	case m.BinaryPath == "" || m.BinaryPath[0] != '/':
		return errors.New("guestd: capsule binary path must be absolute")
	case m.InitPath == "" || m.InitPath[0] != '/':
		return errors.New("guestd: capsule init path must be absolute")
	case m.SleepPath == "" || m.SleepPath[0] != '/':
		return errors.New("guestd: capsule sleep path must be absolute")
	case m.RunnerRoot == "" || m.RunnerRoot[0] != '/':
		return errors.New("guestd: capsule runner root must be absolute")
	case !validCapsuleCgroup(m.CgroupPath):
		return errors.New("guestd: capsule cgroup must be a non-root path below /sys/fs/cgroup")
	}
	return nil
}

func validCapsuleCgroup(path string) bool {
	root := "/sys/fs/cgroup"
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(root, clean)
	return err == nil && filepath.IsAbs(clean) && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (m *CapsuleManager) prepareCgroup() error {
	if err := os.MkdirAll(m.CgroupPath, 0o755); err != nil {
		return fmt.Errorf("guestd: creating capsule cgroup: %w", err)
	}
	if _, err := os.Stat(filepath.Join(m.CgroupPath, "cgroup.events")); err != nil {
		return fmt.Errorf("guestd: capsule cgroup is not cgroup v2: %w", err)
	}
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

// Start creates the initial secretless namespace init for a cold generation.
func (m *CapsuleManager) Start(ctx context.Context) error {
	if err := m.validate(); err != nil {
		return err
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.rootPID > 1 && m.rootReady {
		m.mu.Unlock()
		return nil
	}
	if m.rootPID != 0 {
		m.mu.Unlock()
		return errors.New("guestd: runner capsule is already starting")
	}
	m.rootPID = -1
	m.mu.Unlock()
	cmd := exec.Command(m.BinaryPath, capsuleEnterArgument, m.InitPath, m.SleepPath)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWNS, Setsid: true}
	closeCgroup, err := m.attachCgroup(cmd)
	if err != nil {
		m.mu.Lock()
		m.rootPID = 0
		m.mu.Unlock()
		return err
	}
	defer closeCgroup()
	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		m.rootPID = 0
		m.mu.Unlock()
		return fmt.Errorf("guestd: starting runner capsule: %w", err)
	}
	m.mu.Lock()
	m.rootPID = cmd.Process.Pid
	m.command = cmd
	m.mu.Unlock()
	done := make(chan error, 1)
	go func(pid int) {
		err := cmd.Wait()
		m.mu.Lock()
		if m.rootPID == pid {
			m.rootPID = 0
			m.rootReady = false
			m.command = nil
		}
		m.mu.Unlock()
		done <- err
	}(cmd.Process.Pid)
	if err := waitCapsuleInit(ctx, cmd.Process.Pid, filepath.Base(m.InitPath), done); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("guestd: starting runner capsule: %w", err)
	}
	m.mu.Lock()
	if m.rootPID != cmd.Process.Pid {
		m.mu.Unlock()
		return errors.New("guestd: runner capsule exited during startup")
	}
	m.rootReady = true
	m.mu.Unlock()
	return nil
}

// Reset destroys the complete capsule cgroup and proves it empty. It is the
// transaction boundary between a failed warm restore and a cold start.
func (m *CapsuleManager) Reset(ctx context.Context) error {
	if err := m.validate(); err != nil {
		return err
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := m.prepareCgroup(); err != nil {
		return err
	}
	if err := resetCapsuleBoundary(ctx, 10*time.Millisecond,
		func() error {
			return os.WriteFile(filepath.Join(m.CgroupPath, "cgroup.kill"), []byte("1\n"), 0o200)
		},
		func() (bool, error) { return capsuleCgroupEmpty(m.CgroupPath) },
		func() error { return replaceCapsuleCgroup(m.CgroupPath) },
	); err != nil {
		return err
	}
	m.mu.Lock()
	m.rootPID = 0
	m.rootReady = false
	m.command = nil
	m.mu.Unlock()
	return nil
}

func resetCapsuleBoundary(ctx context.Context, poll time.Duration, kill func() error, empty func() (bool, error), replace func() error) error {
	if err := kill(); err != nil {
		return fmt.Errorf("guestd: killing capsule cgroup: %w", err)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		isEmpty, err := empty()
		if err != nil {
			return err
		}
		if isEmpty {
			if err := replace(); err != nil {
				return fmt.Errorf("guestd: replacing capsule cgroup: %w", err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("guestd: capsule cgroup remained populated: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Ubuntu 24.04's 6.8 kernel has delivered SIGKILL to CLONE_INTO_CGROUP
// children launched immediately after cgroup.kill and populated=0. Holding the
// old directory open while replacing it gives the next capsule a distinct
// cgroup identity and makes that kernel behavior irrelevant.
func replaceCapsuleCgroup(path string) error {
	old, err := os.Open(path)
	if err != nil {
		return err
	}
	defer old.Close()
	oldInfo, err := old.Stat()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(path, "cgroup.events")); err != nil {
		return fmt.Errorf("replacement is not cgroup v2: %w", err)
	}
	newInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	if os.SameFile(oldInfo, newInfo) {
		return errors.New("replacement retained the old cgroup identity")
	}
	return nil
}

func capsuleCgroupEmpty(path string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
	if err != nil {
		return false, fmt.Errorf("guestd: reading capsule cgroup events: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "populated "); ok {
			switch strings.TrimSpace(value) {
			case "0":
				return true, nil
			case "1":
				return false, nil
			}
		}
	}
	return false, errors.New("guestd: capsule cgroup has no populated event")
}

func waitCapsuleInit(ctx context.Context, pid int, name string, done <-chan error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
		if err == nil && processIsNamespaceInit(raw, name) {
			return nil
		}
		if err != nil && !transientCapsuleProcError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return fmt.Errorf("native init exited: %w", err)
		case <-ticker.C:
		}
	}
}

func transientCapsuleProcError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func processIsNamespaceInit(status []byte, name string) bool {
	seenName := false
	seenPIDOne := false
	zombie := false
	for _, line := range strings.Split(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "Name:\t"); ok {
			seenName = strings.TrimSpace(value) == name
		}
		if value, ok := strings.CutPrefix(line, "NSpid:\t"); ok {
			fields := strings.Fields(value)
			seenPIDOne = len(fields) >= 2 && fields[len(fields)-1] == "1"
		}
		if value, ok := strings.CutPrefix(line, "State:\t"); ok {
			zombie = strings.HasPrefix(strings.TrimSpace(value), "Z")
		}
	}
	return seenName && seenPIDOne && !zombie
}

// RootPID returns the host-visible root of the donor capsule after the
// one-shot runner exits. CRIU addresses this PID from guestd's namespace.
func (m *CapsuleManager) RootPID() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rootPID <= 1 || !m.rootReady {
		return 0, errors.New("guestd: no runner capsule is available")
	}
	if err := syscall.Kill(m.rootPID, 0); err != nil {
		return 0, fmt.Errorf("guestd: runner capsule is not alive: %w", err)
	}
	return m.rootPID, nil
}

// IsCapsuleEnter reports the short-lived namespace setup mode. The helper is
// replaced by the native init before any checkpoint is possible.
func IsCapsuleEnter(args []string) bool {
	return len(args) == 4 && args[1] == capsuleEnterArgument
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
	for _, mountpoint := range []string{ProcessMountpoint, "/boot/efi", "/boot", "/tmp"} {
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

// UseRestored adopts a capsule restored before a fresh runner is launched.
func (m *CapsuleManager) UseRestored(_ context.Context, restoredRoot int) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if restoredRoot <= 1 {
		return errors.New("guestd: invalid restored capsule root")
	}
	if err := syscall.Kill(restoredRoot, 0); err != nil {
		return fmt.Errorf("guestd: restored capsule is not alive: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rootPID != 0 {
		return errors.New("guestd: capsule already exists before restore")
	}
	m.rootPID = restoredRoot
	m.rootReady = true
	return nil
}

// PrepareForCheckpoint force-removes any GitHub runner processes while
// preserving arbitrary workload daemons in the capsule. A checkpoint is
// rejected unless the credential-bearing runner is provably absent.
func (m *CapsuleManager) PrepareForCheckpoint(ctx context.Context) error {
	if err := m.validate(); err != nil {
		return err
	}
	rootPID, err := m.RootPID()
	if err != nil {
		return err
	}
	for {
		pids, err := m.runnerProcesses(rootPID)
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("guestd: killing runner process %d: %w", pid, err)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("guestd: runner processes survived forced termination: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (m *CapsuleManager) runnerProcesses(rootPID int) ([]int, error) {
	namespace, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(rootPID), "ns", "pid"))
	if err != nil {
		return nil, fmt.Errorf("guestd: reading capsule PID namespace: %w", err)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == rootPID {
			continue
		}
		candidateNamespace, err := os.Readlink(filepath.Join("/proc", entry.Name(), "ns", "pid"))
		if err != nil || candidateNamespace != namespace {
			continue
		}
		executable, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("guestd: reading process %d executable: %w", pid, err)
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("guestd: reading process %d command line: %w", pid, err)
		}
		if isRunnerProcess(executable, cmdline, m.RunnerRoot) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func isRunnerProcess(executable string, cmdline []byte, root string) bool {
	runnerExecutables := map[string]bool{
		filepath.Join(root, "bin", "Runner.Listener"):    true,
		filepath.Join(root, "bin", "Runner.Worker"):      true,
		filepath.Join(root, "bin", "Runner.Worker.real"): true,
		filepath.Join(root, "bin", "Runner.PluginHost"):  true,
	}
	if runnerExecutables[executable] {
		return true
	}
	for _, argument := range strings.Split(string(cmdline), "\x00") {
		if runnerExecutables[argument] {
			return true
		}
	}
	return false
}
