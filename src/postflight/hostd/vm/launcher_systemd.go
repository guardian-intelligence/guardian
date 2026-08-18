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

// ScopeAPI is the sliver of systemd SystemdLauncher needs.
type ScopeAPI interface {
	// Start wraps argv in a transient scope named unit and returns once the
	// payload is running inside it.
	Start(ctx context.Context, unit, stateDir string, argv []string) error
	// Pids lists the processes in the scope's cgroup; empty when the unit
	// is gone.
	Pids(ctx context.Context, unit string) ([]int, error)
	// Stop asks systemd to kill the scope; stopping an absent unit
	// succeeds.
	Stop(ctx context.Context, unit string) error
}

// SystemdLauncher runs each VM's QEMU in its own transient systemd scope,
// for hosts that are plain machines rather than single-node clusters. A
// scope (not a service) keeps QEMU spawned from hostd's own process tree
// while giving it a cgroup with independent lifetime — hostd restarts never
// kill VMs, and nothing ever restarts a dead QEMU: a dead QEMU is a dead
// slot, collected and refilled.
type SystemdLauncher struct {
	API ScopeAPI
	// QuitGrace bounds the wait for a QMP-acknowledged quit to finish
	// before Kill escalates to stopping the scope.
	QuitGrace time.Duration
	// KillWait bounds Kill's wait for the process to really be gone.
	KillWait time.Duration
}

var _ Launcher = (*SystemdLauncher)(nil)

// NewSystemdLauncher wires the launcher against the system manager.
func NewSystemdLauncher() *SystemdLauncher {
	return &SystemdLauncher{API: SystemdScopes{}}
}

const (
	defaultQuitGrace = 10 * time.Second
	// defaultKillWait mirrors podKillWait: returning while a dying QEMU
	// still holds the root zvol (and its vsock CID) would let the driver's
	// cleanup race it.
	defaultKillWait = 60 * time.Second
)

func scopeUnit(id ID) string { return "pf-vm-" + string(id) + ".scope" }

func (l *SystemdLauncher) quitGrace() time.Duration {
	if l.QuitGrace > 0 {
		return l.QuitGrace
	}
	return defaultQuitGrace
}

func (l *SystemdLauncher) killWait() time.Duration {
	if l.KillWait > 0 {
		return l.KillWait
	}
	return defaultKillWait
}

// Start implements Launcher.
func (l *SystemdLauncher) Start(ctx context.Context, id ID, stateDir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("vm: empty argv")
	}
	return l.API.Start(ctx, scopeUnit(id), stateDir, argv)
}

// ownedPid locates the VM's QEMU inside its scope. A pid counts only while
// its /proc cmdline matches the launched argv, so neither a recycled unit
// name nor a squatter in the scope is ever adopted or killed; strangers
// reports a scope occupied by something that is provably not this VM. A pid
// whose cmdline reads empty or not at all proves nothing — a zombie (QEMU
// exited, parent yet to reap it) and a process mid-exec both look like
// that — so those are ghosts, not strangers: counting them as strangers
// made every destroy of a just-exited VM refuse until the zombie was
// reaped, leaking the slot for the whole window.
func (l *SystemdLauncher) ownedPid(ctx context.Context, unit string, argv []string) (pid int, strangers, ghosts bool, err error) {
	pids, err := l.API.Pids(ctx, unit)
	if err != nil {
		return 0, false, false, err
	}
	want := []byte(strings.Join(argv, "\x00") + "\x00")
	for _, candidate := range pids {
		raw, ok := procCmdline(candidate)
		if !ok || len(raw) == 0 {
			ghosts = true
			continue
		}
		if bytes.Equal(raw, want) {
			return candidate, false, false, nil
		}
		strangers = true
	}
	return 0, strangers, ghosts, nil
}

// Alive implements Launcher.
func (l *SystemdLauncher) Alive(ctx context.Context, id ID, _ string, argv []string) (bool, error) {
	pid, _, _, err := l.ownedPid(ctx, scopeUnit(id), argv)
	if err != nil {
		return false, err
	}
	return pid != 0, nil
}

// Kill implements Launcher: the QMP-driven quit path first, the scope stop
// only for a VM that outlives the grace window (or never answered QMP), and
// in every case a wait for the process to really be gone — the poll-gone
// contract the pod launcher established.
func (l *SystemdLauncher) Kill(ctx context.Context, id ID, stateDir string, argv []string) error {
	unit := scopeUnit(id)
	pid, strangers, ghosts, err := l.ownedPid(ctx, unit, argv)
	if err != nil {
		return err
	}
	if strangers {
		return fmt.Errorf("vm: scope %s is not vm %s's qemu; refusing to stop", unit, id)
	}
	if pid == 0 {
		if ghosts {
			// Only exiting-or-not-yet-exec'd remnants: stop the scope so a
			// pre-exec QEMU cannot outlive the kill, and let systemd finish
			// the teardown as the remnants are reaped.
			return l.API.Stop(ctx, unit)
		}
		return nil
	}
	if l.qmpQuit(ctx, stateDir) {
		gone, err := l.pollGone(ctx, unit, argv, l.quitGrace())
		if err != nil || gone {
			return err
		}
	}
	if err := l.API.Stop(ctx, unit); err != nil {
		return err
	}
	gone, err := l.pollGone(ctx, unit, argv, l.killWait())
	if err != nil {
		return err
	}
	if !gone {
		return fmt.Errorf("vm: scope %s still running after %s", unit, l.killWait())
	}
	return nil
}

// qmpQuit asks QEMU to exit; true only when the command was acknowledged,
// which is what makes waiting out the grace window worthwhile. An
// unreachable socket is normal here — the driver usually quit the VM before
// calling Kill, and a crashed QEMU has no listener at all.
func (l *SystemdLauncher) qmpQuit(ctx context.Context, stateDir string) bool {
	quitCtx, cancel := context.WithTimeout(ctx, qmpDialTimeout)
	defer cancel()
	client, err := dialQMP(quitCtx, qmpSocketPath(stateDir))
	if err != nil {
		return false
	}
	defer client.Close()
	_, err = client.Execute(quitCtx, "quit", nil)
	return err == nil
}

// pollGone waits up to the given bound for the scope to no longer contain
// the VM's process. false means it is still there.
func (l *SystemdLauncher) pollGone(ctx context.Context, unit string, argv []string, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		pid, _, _, err := l.ownedPid(ctx, unit, argv)
		if err != nil {
			return false, err
		}
		if pid == 0 {
			return true, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// SystemdScopes is the real ScopeAPI: systemd-run/systemctl argv against
// the system manager, or the user manager for unprivileged use (tests).
type SystemdScopes struct {
	User bool
}

const (
	scopeStartWait      = 15 * time.Second
	scopeCommandTimeout = 30 * time.Second
	// scopeStopTimeout caps systemd's own SIGTERM→SIGKILL escalation when
	// Kill stops the scope. Unset it would be DefaultTimeoutStopSec (90s),
	// which outlives both the synchronous systemctl stop's command timeout
	// and Kill's poll-gone wait, so a QEMU wedged past SIGTERM would fail
	// Kill even though systemd finishes the job later.
	scopeStopTimeout = 20 * time.Second
)

// Start implements ScopeAPI. In scope mode systemd-run registers the scope,
// moves itself into its cgroup, and execs the payload in place, so the
// payload runs as our direct child; the new session plus the scope cgroup
// are what keep hostd's own stop/restart (and its control group) away from
// it. The return waits for the payload to be execed inside the scope's
// cgroup: registration is asynchronous, and a launch systemd rejected must
// surface here, not as a mystery-dead VM later.
func (s SystemdScopes) Start(ctx context.Context, unit, stateDir string, argv []string) error {
	log, err := os.OpenFile(filepath.Join(stateDir, "qemu.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("vm: opening qemu log: %w", err)
	}
	defer log.Close()
	args := []string{"--scope", "--collect", "--quiet",
		"--property=TimeoutStopSec=" + scopeStopTimeout.String(), "--unit=" + unit}
	if s.User {
		args = append([]string{"--user"}, args...)
	}
	args = append(args, "--")
	args = append(args, argv...)
	cmd := exec.Command("systemd-run", args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("vm: starting systemd-run: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(scopeStartWait)
	for {
		pids, err := s.Pids(ctx, unit)
		if err == nil {
			for _, pid := range pids {
				if cmdlineMatches(pid, argv) {
					return nil
				}
			}
		}
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("vm: systemd-run for %s: %w (see qemu.log)", unit, err)
			}
			// The payload already ran and exited cleanly; the driver will
			// observe the process gone and collect the VM.
			return nil
		default:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vm: scope %s never appeared", unit)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Pids implements ScopeAPI: resolve the unit's cgroup from systemd, then
// read cgroup.procs. systemctl show reports absent units as LoadState
// not-found with exit 0, so a gone scope is a clean empty answer while an
// unreachable manager stays an error — Alive must never read a probe
// failure as a dead VM.
func (s SystemdScopes) Pids(ctx context.Context, unit string) ([]int, error) {
	out, err := s.systemctl(ctx, "show", "--property=LoadState,ControlGroup", unit)
	if err != nil {
		return nil, err
	}
	properties := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			properties[key] = value
		}
	}
	if properties["LoadState"] != "loaded" || properties["ControlGroup"] == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", properties["ControlGroup"], "cgroup.procs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // the scope was collected between show and read
	}
	if err != nil {
		return nil, fmt.Errorf("vm: reading cgroup of %s: %w", unit, err)
	}
	var pids []int
	for _, field := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// Stop implements ScopeAPI. systemctl stop is synchronous, and stopping a
// unit that already vanished is the idempotent success the contract asks
// for.
func (s SystemdScopes) Stop(ctx context.Context, unit string) error {
	_, err := s.systemctl(ctx, "stop", unit)
	if err != nil && strings.Contains(err.Error(), "not loaded") {
		return nil
	}
	return err
}

func (s SystemdScopes) systemctl(ctx context.Context, args ...string) (string, error) {
	verb := args[0]
	if s.User {
		args = append([]string{"--user"}, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, scopeCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("vm: systemctl %s: %s: %w", verb, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}
