package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The conformance suite runs the real QEMU driver — ProcessLauncher, real
// zvols, real QMP — through the same lifecycle expectations the sim asserts
// of the fake. CI has neither ZFS nor KVM; on a prepared host set
//
//	HOSTD_QEMU_TEST_ROOT   scratch dataset root (created and destroyed)
//	HOSTD_QEMU_TEST_IMAGE  bootable golden snapshot, e.g. tank/vm-golden@gold
//	HOSTD_QEMU_TEST_QEMU   QEMU binary, e.g. /usr/bin/qemu-system-x86_64
//	HOSTD_QEMU_TEST_FIRMWARE pinned AmdSev OVMF.fd
//
// and run the package tests as root.
func conformanceDriver(t *testing.T) (*QEMU, *scriptedGuest) {
	t.Helper()
	root := os.Getenv("HOSTD_QEMU_TEST_ROOT")
	image := os.Getenv("HOSTD_QEMU_TEST_IMAGE")
	qemuPath := os.Getenv("HOSTD_QEMU_TEST_QEMU")
	firmware := os.Getenv("HOSTD_QEMU_TEST_FIRMWARE")
	if root == "" || image == "" || qemuPath == "" || firmware == "" {
		t.Skip("set HOSTD_QEMU_TEST_{ROOT,IMAGE,QEMU,FIRMWARE} to run the QEMU conformance suite")
	}
	if _, err := exec.LookPath("zfs"); err != nil {
		t.Skip("zfs binary not available")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skipf("no KVM: %v", err)
	}
	ctx := context.Background()
	if out, err := zfsCommand(ctx, "list", "-H", "-o", "name", root); err != nil || strings.TrimSpace(out) != root {
		if _, err := zfsCommand(ctx, "create", "-p", root); err != nil {
			t.Fatalf("creating scratch root %s: %v", root, err)
		}
	}
	t.Cleanup(func() {
		_, _ = zfsCommand(context.Background(), "destroy", "-r", root)
	})
	guest := newScriptedGuest()
	driver, err := NewQEMU(Config{
		StateRoot:   shortTempDir(t),
		QEMUPath:    qemuPath,
		Firmware:    firmware,
		DatasetRoot: root,
		Classes: map[Class]ClassConfig{
			testClass: {CPUs: 2, MemoryMiB: 2048, Image: image},
		},
		Launcher: ProcessLauncher{},
		Guest:    guest,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return driver, guest
}

func zfsCommand(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "zfs", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &conformanceZFSError{args: args, stderr: stderr.String(), err: err}
	}
	return stdout.String(), nil
}

type conformanceZFSError struct {
	args   []string
	stderr string
	err    error
}

func (e *conformanceZFSError) Error() string {
	return "zfs " + strings.Join(e.args, " ") + ": " + strings.TrimSpace(e.stderr) + ": " + e.err.Error()
}

// waitFor polls until the condition holds, failing the test at the deadline.
func waitFor(t *testing.T, what string, timeout time.Duration, condition func() (bool, error)) time.Duration {
	t.Helper()
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		ok, err := condition()
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if ok {
			return time.Since(start)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func vmRunningViaQMP(ctx context.Context, driver *QEMU, id ID) func() (bool, error) {
	return func() (bool, error) {
		client, err := dialQMP(ctx, qmpSocketPath(driver.stateDir(id)))
		if err != nil {
			return false, nil // QEMU still coming up
		}
		defer client.Close()
		result, err := client.Execute(ctx, "query-status", nil)
		if err != nil {
			return false, nil
		}
		var reply struct {
			Status  string `json:"status"`
			Running bool   `json:"running"`
		}
		if err := json.Unmarshal(result, &reply); err != nil {
			return false, err
		}
		return reply.Running, nil
	}
}

func guestBooted(driver *QEMU, id ID) func() (bool, error) {
	return func() (bool, error) {
		console, err := os.ReadFile(serialLogPath(driver.stateDir(id)))
		if err != nil {
			return false, nil
		}
		return bytes.Contains(console, []byte("login:")), nil
	}
}

func datasetExists(ctx context.Context, dataset string) (bool, error) {
	_, err := zfsCommand(ctx, "list", "-H", "-o", "name", dataset)
	if err != nil {
		if zfsErr, ok := err.(*conformanceZFSError); ok && strings.Contains(zfsErr.stderr, "does not exist") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// TestConformanceLifecycle drives one VM through the full arc the agent
// depends on: launch → running via QMP → warm via guestd → assign with a
// real zvol clone hot-attached by serial → ready → exited → detach releases
// the volume → destroy leaves nothing.
func TestConformanceLifecycle(t *testing.T) {
	driver, guest := conformanceDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	const id = ID("conf-lifecycle")

	launchStart := time.Now()
	if err := driver.Launch(ctx, id, testClass); err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { _ = driver.Destroy(context.Background(), id) })

	status, err := driver.Status(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseBooting {
		t.Fatalf("phase %s right after launch, want booting", status.Phase)
	}
	toRunning := waitFor(t, "QMP reports running", 60*time.Second, vmRunningViaQMP(ctx, driver, id))
	t.Logf("launch → QMP running in %s (total since launch %s)", toRunning, time.Since(launchStart))
	toLogin := waitFor(t, "guest login prompt on serial console", 180*time.Second, guestBooted(driver, id))
	t.Logf("guest booted to login prompt in %s", toLogin)

	// Warm is the guest's word, not QEMU's.
	guest.set(id, GuestObservation{Hello: true})
	status, err = driver.Status(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseWarm {
		t.Fatalf("phase %s after hello, want warm", status.Phase)
	}

	// A real workspace: a zvol clone of the golden snapshot.
	workspace := driver.cfg.DatasetRoot + "/ws-conf-lifecycle"
	if err := driver.disks.Ensure(ctx, workspace, driver.cfg.Classes[testClass].Image); err != nil {
		t.Fatalf("cloning workspace: %v", err)
	}
	process := driver.cfg.DatasetRoot + "/process-conf-lifecycle"
	if err := driver.disks.Ensure(ctx, process, driver.cfg.Classes[testClass].Image); err != nil {
		t.Fatalf("cloning process volume: %v", err)
	}
	tool := driver.cfg.DatasetRoot + "/tool-conf-lifecycle"
	if err := driver.disks.Ensure(ctx, tool, driver.cfg.Classes[testClass].Image); err != nil {
		t.Fatalf("cloning tool volume: %v", err)
	}
	preparation := Preparation{Lease: "lease-conf", JITConfig: "jit-blob"}
	rendezvous := Rendezvous{
		Lease:               "lease-conf",
		WorkspaceDevice:     zvolDevicePath(workspace),
		WorkspaceMountpoint: "/opt/actions-runner/_work/widget/widget",
		ToolDevice:          zvolDevicePath(tool),
		ProcessDevice:       zvolDevicePath(process),
	}
	attachStart := time.Now()
	if err := driver.Rendezvous(ctx, id, rendezvous); err != nil {
		t.Fatalf("rendezvous: %v", err)
	}
	t.Logf("rendezvous (hot-attach + deliver) in %s", time.Since(attachStart))
	if err := driver.Rendezvous(ctx, id, rendezvous); err != nil {
		t.Fatalf("repeat rendezvous: %v", err)
	}
	guest.set(id, GuestObservation{Hello: true, MountsReady: true})
	if err := driver.Prepare(ctx, id, preparation); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	client, err := dialQMP(ctx, qmpSocketPath(driver.stateDir(id)))
	if err != nil {
		t.Fatal(err)
	}
	attached, err := blockdevPresent(ctx, client, workspaceNode)
	if err != nil || !attached {
		t.Fatalf("workspace blockdev present=%t err=%v", attached, err)
	}
	present, err := devicePresent(ctx, client, workspaceDevice)
	if err != nil || !present {
		t.Fatalf("workspace qdev present=%t err=%v", present, err)
	}
	client.Close()

	deliveries := guest.rendezvouses(id)
	if len(deliveries) == 0 || deliveries[0].Lease != "lease-conf" ||
		len(deliveries[0].Mounts) != 3 || deliveries[0].Mounts[0].Serial != toolNode || deliveries[0].Mounts[1].Serial != workspaceNode || deliveries[0].Mounts[2].Serial != processNode {
		t.Fatalf("deliveries %+v", deliveries)
	}
	status, err = driver.Status(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseAssigned || status.Lease != "lease-conf" {
		t.Fatalf("status %+v after assign", status)
	}

	guest.set(id, GuestObservation{Hello: true, RunnerRegistered: true})
	if status, _ = driver.Status(ctx, id); status.Phase != PhaseListening {
		t.Fatalf("phase %s, want listening", status.Phase)
	}
	guest.set(id, GuestObservation{Hello: true, RunnerExited: true, ExitCode: 7})
	if status, _ = driver.Status(ctx, id); status.Phase != PhaseExited || status.ExitCode != 7 {
		t.Fatalf("status %+v, want exited 7", status)
	}

	// Detach: the guest acks the unplug, the blockdev goes, and the zvol is
	// destroyable while the VM still runs — the release the seal path needs.
	client, err = dialQMP(ctx, qmpSocketPath(driver.stateDir(id)))
	if err != nil {
		t.Fatal(err)
	}
	detachStart := time.Now()
	if err := driver.detachVolume(ctx, client, workspaceNode, workspaceDevice); err != nil {
		t.Fatalf("detach: %v", err)
	}
	t.Logf("detach in %s", time.Since(detachStart))
	if attached, err := blockdevPresent(ctx, client, workspaceNode); err != nil || attached {
		t.Fatalf("workspace blockdev present=%t err=%v after detach", attached, err)
	}
	client.Close()
	if err := driver.disks.Destroy(ctx, workspace); err != nil {
		t.Fatalf("destroying detached workspace while vm runs: %v", err)
	}

	destroyStart := time.Now()
	if err := driver.Destroy(ctx, id); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	t.Logf("destroy in %s", time.Since(destroyStart))
	if status, _ = driver.Status(ctx, id); status.Phase != PhaseGone {
		t.Fatalf("phase %s after destroy, want gone", status.Phase)
	}
	statuses, err := driver.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("listed %+v after destroy", statuses)
	}
	if exists, err := datasetExists(ctx, driver.rootDataset(id)); err != nil || exists {
		t.Fatalf("root dataset exists=%t err=%v after destroy", exists, err)
	}
	if _, err := os.Stat(driver.stateDir(id)); !os.IsNotExist(err) {
		t.Fatalf("state dir survived destroy: %v", err)
	}
	if err := driver.Destroy(ctx, id); err != nil {
		t.Fatalf("second destroy: %v", err)
	}
}

// TestConformanceAdoption proves a restarted hostd adopts running VMs: a
// brand-new driver instance over the same state root sees the VM, lease
// binding intact, and can destroy it.
func TestConformanceAdoption(t *testing.T) {
	first, _ := conformanceDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	const id = ID("conf-adopt")

	if err := first.Launch(ctx, id, testClass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Destroy(context.Background(), id) })
	waitFor(t, "QMP reports running", 60*time.Second, vmRunningViaQMP(ctx, first, id))

	workspace := first.cfg.DatasetRoot + "/ws-conf-adopt"
	if err := first.disks.Ensure(ctx, workspace, first.cfg.Classes[testClass].Image); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.disks.Destroy(context.Background(), workspace) })
	if err := first.Prepare(ctx, id, Preparation{Lease: "lease-adopt", JITConfig: "jit"}); err != nil {
		t.Fatal(err)
	}
	originalMeta, err := first.readMeta(id)
	if err != nil {
		t.Fatal(err)
	}

	// The restart: a fresh driver over the same state root, no in-memory
	// carryover — same config, new Guest seam, new everything.
	secondGuest := newScriptedGuest()
	second, err := NewQEMU(Config{
		StateRoot:   first.cfg.StateRoot,
		QEMUPath:    first.cfg.QEMUPath,
		Firmware:    first.cfg.Firmware,
		DatasetRoot: first.cfg.DatasetRoot,
		Classes:     first.cfg.Classes,
		Launcher:    ProcessLauncher{},
		Guest:       secondGuest,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := second.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("adopted %d vms, want 1: %+v", len(statuses), statuses)
	}
	adopted := statuses[0]
	if adopted.ID != id || adopted.Lease != "lease-adopt" || adopted.Class != testClass || adopted.Phase != PhaseAssigned {
		t.Fatalf("adopted %+v", adopted)
	}
	adoptedMeta, err := second.readMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if adoptedMeta.CID != originalMeta.CID || adoptedMeta.ArgvSHA256 != originalMeta.ArgvSHA256 {
		t.Fatalf("adoption changed identity: %+v vs %+v", adoptedMeta, originalMeta)
	}

	// Destroy through the adopted instance: process, dataset, state all go.
	if err := second.Destroy(ctx, id); err != nil {
		t.Fatalf("destroy from adopted driver: %v", err)
	}
	alive, err := ProcessLauncher{}.Alive(ctx, id, second.stateDir(id), adoptedMeta.Argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v after adopted destroy", alive, err)
	}
	if exists, err := datasetExists(ctx, second.rootDataset(id)); err != nil || exists {
		t.Fatalf("root dataset exists=%t err=%v after adopted destroy", exists, err)
	}
}

// TestConformanceCorruptMetaRecovery: an externally corrupted meta.json
// must never wedge the host. Status quarantines the VM, launching anything
// new is refused while the corrupt VM may hold an unknown vsock CID, and
// List collects it — QMP quit as the identity-safe kill, the real QEMU
// process gone, dataset and state dir destroyed — after which launches
// succeed again.
func TestConformanceCorruptMetaRecovery(t *testing.T) {
	driver, _ := conformanceDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const id = ID("conf-corrupt")

	if err := driver.Launch(ctx, id, testClass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Destroy(context.Background(), id) })
	waitFor(t, "QMP reports running", 60*time.Second, vmRunningViaQMP(ctx, driver, id))
	record, err := driver.readMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driver.metaPath(id), []byte(`{"id": "conf-corrupt", "cid": "three`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := driver.Status(ctx, id)
	if err != nil {
		t.Fatalf("status on corrupt meta: %v", err)
	}
	if status.Phase != PhaseGone {
		t.Fatalf("phase %s, want gone", status.Phase)
	}
	if err := driver.Launch(ctx, "conf-corrupt-2", testClass); err == nil {
		t.Fatal("launched while a corrupt meta may hold an unknown cid")
	}

	statuses, err := driver.List(ctx)
	if err != nil {
		t.Fatalf("list with corrupt meta present: %v", err)
	}
	for _, status := range statuses {
		if status.ID == id {
			t.Fatalf("corrupt vm still listed: %+v", status)
		}
	}
	waitFor(t, "qemu process death", 30*time.Second, func() (bool, error) {
		return scanProcForArgv(record.Argv) == 0, nil
	})
	if exists, err := datasetExists(ctx, driver.rootDataset(id)); err != nil || exists {
		t.Fatalf("root dataset exists=%t err=%v after collection", exists, err)
	}
	if _, err := os.Stat(driver.stateDir(id)); !os.IsNotExist(err) {
		t.Fatalf("state dir survived collection: %v", err)
	}

	if err := driver.Launch(ctx, "conf-corrupt-2", testClass); err != nil {
		t.Fatalf("launch after collection: %v", err)
	}
	if err := driver.Destroy(ctx, "conf-corrupt-2"); err != nil {
		t.Fatal(err)
	}
}

// TestConformanceCrashCollection: a VM whose QEMU dies out from under the
// driver is collected by List — leftovers destroyed, nothing reported.
func TestConformanceCrashCollection(t *testing.T) {
	driver, _ := conformanceDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const id = ID("conf-crash")

	if err := driver.Launch(ctx, id, testClass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Destroy(context.Background(), id) })
	waitFor(t, "QMP reports running", 60*time.Second, vmRunningViaQMP(ctx, driver, id))

	record, err := driver.readMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := readPidFile(driver.stateDir(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "process death", 10*time.Second, func() (bool, error) {
		alive, err := ProcessLauncher{}.Alive(ctx, id, driver.stateDir(id), record.Argv)
		if err != nil {
			return false, err
		}
		return !alive, nil
	})

	statuses, err := driver.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.ID == id {
			t.Fatalf("crashed vm still listed: %+v", status)
		}
	}
	if exists, err := datasetExists(ctx, driver.rootDataset(id)); err != nil || exists {
		t.Fatalf("root dataset exists=%t err=%v after crash collection", exists, err)
	}
	if _, err := os.Stat(driver.stateDir(id)); !os.IsNotExist(err) {
		t.Fatalf("state dir survived crash collection: %v", err)
	}
}
