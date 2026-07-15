package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessLauncherLifecycle(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	stateDir := t.TempDir()
	argv := []string{sleep, "300"}
	launcher := ProcessLauncher{}
	ctx := context.Background()
	if err := launcher.Start(ctx, "vm-a", stateDir, argv); err != nil {
		t.Fatalf("start: %v", err)
	}
	alive, err := launcher.Alive(ctx, "vm-a", stateDir, argv)
	if err != nil || !alive {
		t.Fatalf("alive=%t err=%v after start", alive, err)
	}
	if err := launcher.Kill(ctx, "vm-a", stateDir, argv); err != nil {
		t.Fatalf("kill: %v", err)
	}
	alive, err = launcher.Alive(ctx, "vm-a", stateDir, argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v after kill", alive, err)
	}
	if err := launcher.Kill(ctx, "vm-a", stateDir, argv); err != nil {
		t.Fatalf("second kill: %v", err)
	}
}

// TestProcessLauncherSurvivesArgvMismatch is the pid-reuse guard: a recorded
// pid whose /proc cmdline is not the VM's argv must read as dead and must
// never be killed.
func TestProcessLauncherSurvivesArgvMismatch(t *testing.T) {
	stateDir := t.TempDir()
	// This test process's own pid: definitely alive, definitely not QEMU.
	if err := os.WriteFile(filepath.Join(stateDir, "launcher.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	argv := []string{"/usr/bin/qemu-system-x86_64", "-nodefaults"}
	launcher := ProcessLauncher{}
	ctx := context.Background()
	alive, err := launcher.Alive(ctx, "vm-a", stateDir, argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v for a recycled pid", alive, err)
	}
	if err := launcher.Kill(ctx, "vm-a", stateDir, argv); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// Still here: Kill refused to signal the stranger.
}

func TestProcessLauncherAliveWithoutPidFile(t *testing.T) {
	launcher := ProcessLauncher{}
	alive, err := launcher.Alive(context.Background(), "vm-a", t.TempDir(), []string{"sleep", "1"})
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v without a pid file", alive, err)
	}
}

// TestProcessLauncherDetachesIntoOwnSession proves the setsid detachment
// that lets the child ignore its parent's fate.
func TestProcessLauncherDetachesIntoOwnSession(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh binary: %v", err)
	}
	stateDir := t.TempDir()
	argv := []string{sleep, "300"}
	launcher := ProcessLauncher{}
	ctx := context.Background()
	if err := launcher.Start(ctx, "vm-a", stateDir, argv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launcher.Kill(ctx, "vm-a", stateDir, argv) })
	pid, err := readPidFile(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	// Session id equal to its own pid means setsid took: no controlling
	// terminal, not in this process's session, unaffected by our signals.
	out, err := exec.Command(sh, "-c", "ps -o sid= -p "+strconv.Itoa(pid)).Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	sid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parsing sid %q: %v", out, err)
	}
	if sid != pid {
		t.Fatalf("child sid %d != pid %d; not detached", sid, pid)
	}
	// And it is still alive well after Start returned.
	time.Sleep(100 * time.Millisecond)
	alive, err := launcher.Alive(ctx, "vm-a", stateDir, argv)
	if err != nil || !alive {
		t.Fatalf("alive=%t err=%v", alive, err)
	}
}
