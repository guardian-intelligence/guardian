package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeScopes is an in-memory ScopeAPI: tests park real processes' pids in
// it so the launcher's cmdline identity checks run against real /proc
// entries without needing systemd.
type fakeScopes struct {
	mu      sync.Mutex
	pids    map[string][]int
	stopped []string
	onStop  func(unit string)
}

func newFakeScopes() *fakeScopes {
	return &fakeScopes{pids: map[string][]int{}}
}

func (f *fakeScopes) Start(_ context.Context, _ string, _ string, _ []string) error {
	return nil
}

func (f *fakeScopes) Pids(_ context.Context, unit string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.pids[unit]...), nil
}

func (f *fakeScopes) Stop(_ context.Context, unit string) error {
	f.mu.Lock()
	f.stopped = append(f.stopped, unit)
	onStop := f.onStop
	f.mu.Unlock()
	if onStop != nil {
		onStop(unit)
	}
	return nil
}

// spawnStub starts a throwaway process whose exact argv stands in for a
// VM's QEMU invocation.
func spawnStub(t *testing.T, duration string) (*exec.Cmd, []string) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	argv := []string{sleep, duration}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	// Under load the child's argv takes a moment to land in /proc; every
	// test below keys identity on it, so wait until it is observable.
	deadline := time.Now().Add(5 * time.Second)
	for !cmdlineMatches(cmd.Process.Pid, argv) {
		if time.Now().After(deadline) {
			t.Fatalf("stub pid %d never showed argv %q", cmd.Process.Pid, argv)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cmd, argv
}

// quitStub serves a QMP endpoint at stateDir whose quit handler runs
// onQuit before acknowledging, standing in for a QEMU that exits (or
// refuses to) on quit.
func quitStub(t *testing.T, stateDir string, onQuit func()) {
	t.Helper()
	startQMPServer(t, &qmpServer{
		socket: filepath.Join(stateDir, "qmp.sock"),
		handle: func(command string, _ json.RawMessage) ([]string, string) {
			if command == "quit" && onQuit != nil {
				onQuit()
			}
			return nil, `{"return": {}, "id": %d}`
		},
	})
}

func TestSystemdLauncherKillQuitPathAvoidsScopeStop(t *testing.T) {
	cmd, argv := spawnStub(t, "300")
	stateDir := shortTempDir(t)
	quitStub(t, stateDir, func() { _ = cmd.Process.Kill() })
	fake := newFakeScopes()
	fake.pids[scopeUnit("vm-a")] = []int{cmd.Process.Pid}
	launcher := &SystemdLauncher{API: fake, QuitGrace: 5 * time.Second, KillWait: time.Second}
	if err := launcher.Kill(context.Background(), "vm-a", stateDir, argv); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("scope stopped %v; the acknowledged quit was enough", fake.stopped)
	}
}

func TestSystemdLauncherKillEscalatesToScopeStopAfterGrace(t *testing.T) {
	cmd, argv := spawnStub(t, "300")
	stateDir := shortTempDir(t)
	// The guest acknowledges quit but never exits.
	quitStub(t, stateDir, nil)
	fake := newFakeScopes()
	fake.pids[scopeUnit("vm-a")] = []int{cmd.Process.Pid}
	fake.onStop = func(string) { _ = cmd.Process.Kill() }
	launcher := &SystemdLauncher{API: fake, QuitGrace: 50 * time.Millisecond, KillWait: 5 * time.Second}
	if err := launcher.Kill(context.Background(), "vm-a", stateDir, argv); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if want := []string{scopeUnit("vm-a")}; len(fake.stopped) != 1 || fake.stopped[0] != want[0] {
		t.Fatalf("stopped %v, want %v", fake.stopped, want)
	}
}

// TestSystemdLauncherKillWithoutQMPGoesStraightToStop: an unreachable QMP
// socket must not burn the grace window — QuitGrace is set absurdly high so
// taking it would hang the test.
func TestSystemdLauncherKillWithoutQMPGoesStraightToStop(t *testing.T) {
	cmd, argv := spawnStub(t, "300")
	fake := newFakeScopes()
	fake.pids[scopeUnit("vm-a")] = []int{cmd.Process.Pid}
	fake.onStop = func(string) { _ = cmd.Process.Kill() }
	launcher := &SystemdLauncher{API: fake, QuitGrace: time.Hour, KillWait: 5 * time.Second}
	if err := launcher.Kill(context.Background(), "vm-a", shortTempDir(t), argv); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if len(fake.stopped) != 1 {
		t.Fatalf("stopped %v, want exactly the vm's scope", fake.stopped)
	}
}

// TestSystemdLauncherRefusesStrangerScope is the unit-collision guard: a
// scope under the VM's name whose process is not the VM's argv must neither
// be adopted nor killed.
func TestSystemdLauncherRefusesStrangerScope(t *testing.T) {
	fake := newFakeScopes()
	// This test process's own pid: definitely alive, definitely not QEMU.
	fake.pids[scopeUnit("vm-a")] = []int{os.Getpid()}
	argv := []string{"/usr/bin/qemu-system-x86_64", "-nodefaults"}
	launcher := &SystemdLauncher{API: fake}
	ctx := context.Background()
	alive, err := launcher.Alive(ctx, "vm-a", "", argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v for a stranger's scope", alive, err)
	}
	err = launcher.Kill(ctx, "vm-a", shortTempDir(t), argv)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("kill of a stranger's scope: %v", err)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("stopped %v; the stranger must be left alone", fake.stopped)
	}
}

// TestSystemdLauncherKillReclaimsZombieScope is the just-exited-VM window:
// QEMU has exited but its parent has not reaped it yet, so the scope's only
// pid is a zombie with an empty cmdline. That proves nothing about
// strangers — Kill must reclaim the scope, not refuse and leak the slot
// until the reap.
func TestSystemdLauncherKillReclaimsZombieScope(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	argv := []string{sleep, "0"}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	// Deliberately unreaped: wait until the child is a zombie (cmdline
	// reads empty once the process has exited).
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, ok := procCmdline(cmd.Process.Pid)
		if ok && len(raw) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stub pid %d never became a zombie", cmd.Process.Pid)
		}
		time.Sleep(5 * time.Millisecond)
	}

	fake := newFakeScopes()
	fake.pids[scopeUnit("vm-a")] = []int{cmd.Process.Pid}
	launcher := &SystemdLauncher{API: fake}
	ctx := context.Background()
	if alive, err := launcher.Alive(ctx, "vm-a", "", argv); err != nil || alive {
		t.Fatalf("alive=%t err=%v for a zombie scope", alive, err)
	}
	if err := launcher.Kill(ctx, "vm-a", shortTempDir(t), argv); err != nil {
		t.Fatalf("kill of a zombie scope: %v", err)
	}
	if len(fake.stopped) != 1 {
		t.Fatalf("stopped %v; the zombie scope must be stopped for teardown", fake.stopped)
	}
}

func TestSystemdLauncherKillAbsentScope(t *testing.T) {
	launcher := &SystemdLauncher{API: newFakeScopes()}
	if err := launcher.Kill(context.Background(), "vm-a", shortTempDir(t), []string{"/usr/bin/qemu-system-x86_64"}); err != nil {
		t.Fatalf("kill of an absent scope: %v", err)
	}
}

// TestSystemdLauncherAdoptionProbe is the restart path: Alive adopts the
// scope whose process runs the recorded argv and quarantines everything
// else as dead.
func TestSystemdLauncherAdoptionProbe(t *testing.T) {
	cmd, argv := spawnStub(t, "300")
	fake := newFakeScopes()
	fake.pids[scopeUnit("vm-a")] = []int{cmd.Process.Pid}
	launcher := &SystemdLauncher{API: fake}
	ctx := context.Background()
	alive, err := launcher.Alive(ctx, "vm-a", "", argv)
	if err != nil || !alive {
		t.Fatalf("alive=%t err=%v for the recorded argv", alive, err)
	}
	mismatched := append(append([]string(nil), argv...), "-extra")
	alive, err = launcher.Alive(ctx, "vm-a", "", mismatched)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v for a mismatched argv", alive, err)
	}
	alive, err = launcher.Alive(ctx, "vm-b", "", argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v for an absent scope", alive, err)
	}
}

// TestSystemdScopesLifecycle drives the real ScopeAPI when a systemd
// manager is reachable, self-skipping otherwise (CI sandboxes have no bus).
func TestSystemdScopesLifecycle(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skipf("no systemd-run: %v", err)
	}
	scopes := SystemdScopes{User: os.Geteuid() != 0}
	canaryArgs := []string{"--scope", "--collect", "--quiet"}
	if scopes.User {
		canaryArgs = append([]string{"--user"}, canaryArgs...)
	}
	if out, err := exec.Command("systemd-run", append(canaryArgs, "--", "true")...).CombinedOutput(); err != nil {
		t.Skipf("systemd manager unreachable: %v: %s", err, out)
	}

	id := ID(fmt.Sprintf("test-%d", os.Getpid()))
	stateDir := shortTempDir(t)
	// A duration no other process on the host would be sleeping for.
	argv := []string{sleep, "271830"}
	launcher := &SystemdLauncher{API: scopes, QuitGrace: time.Second, KillWait: 30 * time.Second}
	ctx := context.Background()
	if err := launcher.Start(ctx, id, stateDir, argv); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = scopes.Stop(context.Background(), scopeUnit(id)) })
	alive, err := launcher.Alive(ctx, id, stateDir, argv)
	if err != nil || !alive {
		t.Fatalf("alive=%t err=%v after start", alive, err)
	}
	if err := launcher.Kill(ctx, id, stateDir, argv); err != nil {
		t.Fatalf("kill: %v", err)
	}
	alive, err = launcher.Alive(ctx, id, stateDir, argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v after kill", alive, err)
	}
	if err := launcher.Kill(ctx, id, stateDir, argv); err != nil {
		t.Fatalf("second kill: %v", err)
	}
}
