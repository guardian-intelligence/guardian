package guestd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// loopbackDevice materializes a loopback block device over a sparse file,
// skipping wherever the substrate is missing (CI sandboxes have no root, no
// /dev/loop-control, and none of the mkfs/blkid tooling).
func loopbackDevice(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("loopback mount conformance needs root")
	}
	for _, binary := range []string{"losetup", "mkfs.ext4", "blkid"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s not available", binary)
		}
	}
	if _, err := os.Stat("/dev/loop-control"); err != nil {
		t.Skipf("no loop control device: %v", err)
	}
	backing := filepath.Join(t.TempDir(), "workspace.img")
	file, err := os.Create(backing)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	out, err := exec.Command("losetup", "--find", "--show", backing).CombinedOutput()
	if err != nil {
		t.Skipf("losetup: %v: %s", err, out)
	}
	device := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("losetup", "-d", device).Run() })
	return device
}

// liveMountOptions returns the kernel's recorded options for a mountpoint.
func liveMountOptions(t *testing.T, mountpoint string) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && unescapeMountPath(fields[1]) == mountpoint {
			return fields[3]
		}
	}
	t.Fatalf("%s not in /proc/self/mounts", mountpoint)
	return ""
}

// TestRealSystemLoopbackMountConvergence drives the production System
// against a real block device: blank probe, mkfs, a mount that must carry
// discard through to the kernel, busy unmount, adoption.
func TestRealSystemLoopbackMountConvergence(t *testing.T) {
	device := loopbackDevice(t)
	ctx := context.Background()
	owner := "nobody"
	if _, err := user.Lookup(owner); err != nil {
		owner = "root"
	}
	system := RealSystem{RunnerUser: owner}

	if blank, err := system.IsBlank(ctx, device); err != nil || !blank {
		t.Fatalf("fresh device blank=%v err=%v, want blank", blank, err)
	}
	if err := system.MakeFilesystem(ctx, device, "ext4"); err != nil {
		t.Fatal(err)
	}
	if blank, err := system.IsBlank(ctx, device); err != nil || blank {
		t.Fatalf("formatted device blank=%v err=%v, want not blank", blank, err)
	}

	// A nested mountpoint exercises the created-parent handoff.
	root := t.TempDir()
	mountpoint := filepath.Join(root, "repo", "repo")
	err := system.Mount(ctx, device, mountpoint, "ext4", []string{"discard", "noatime", "nodev", "nosuid"})
	if errors.Is(err, unix.EPERM) {
		t.Skipf("mount not permitted here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(mountpoint, unix.MNT_DETACH) })

	options := liveMountOptions(t, mountpoint)
	for _, option := range []string{"discard", "noatime", "nodev", "nosuid"} {
		if !strings.Contains(options, option) {
			t.Fatalf("live mount options %q missing %s", options, option)
		}
	}
	if mounted, err := system.IsMounted(mountpoint); err != nil || !mounted {
		t.Fatalf("mounted=%v err=%v, want mounted", mounted, err)
	}

	if err := system.Adopt(mountpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mountpoint, WorkspaceMarker)); err != nil {
		t.Fatalf("workspace marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mountpoint, "lost+found")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost+found survived adoption: %v", err)
	}
	account, err := user.Lookup(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{mountpoint, filepath.Dir(mountpoint)} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if uid := info.Sys().(*syscall.Stat_t).Uid; strconv.FormatUint(uint64(uid), 10) != account.Uid {
			t.Fatalf("%s owned by uid %d, want %s (%s)", dir, uid, account.Uid, owner)
		}
	}

	// Busy unmount: an open file holds the filesystem, then releases it.
	straggler, err := os.Create(filepath.Join(mountpoint, "straggler"))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Unmount(mountpoint); err == nil {
		t.Fatal("unmount succeeded under an open file")
	}
	straggler.Close()
	if err := system.Unmount(mountpoint); err != nil {
		t.Fatalf("unmount after release: %v", err)
	}
	if mounted, err := system.IsMounted(mountpoint); err != nil || mounted {
		t.Fatalf("mounted=%v err=%v after unmount, want unmounted", mounted, err)
	}
}

// TestRealSystemLoopbackEncryptedConvergence drives the production LUKS2
// ladder against a real block device: format, open, mkfs on the mapper,
// mount, adopt — then proves the raw device carries only ciphertext and
// that a reopen with the same key sees the existing filesystem.
func TestRealSystemLoopbackEncryptedConvergence(t *testing.T) {
	device := loopbackDevice(t)
	if _, err := exec.LookPath("cryptsetup"); err != nil {
		t.Skip("cryptsetup not available")
	}
	ctx := context.Background()
	system := RealSystem{RunnerUser: "root"}
	key, err := workspaceKey(EncryptionDev)
	if err != nil {
		t.Fatal(err)
	}

	if luks, err := system.IsLUKS(ctx, device); err != nil || luks {
		t.Fatalf("fresh device luks=%v err=%v, want plain", luks, err)
	}
	if err := system.Discard(ctx, device); err != nil {
		t.Fatal(err)
	}
	if err := system.FormatLUKS(ctx, device, key); err != nil {
		t.Fatal(err)
	}
	if luks, err := system.IsLUKS(ctx, device); err != nil || !luks {
		t.Fatalf("formatted device luks=%v err=%v, want luks", luks, err)
	}
	name := "pf-loopback-test"
	mapper, err := system.OpenLUKS(ctx, device, name, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("cryptsetup", "close", name).Run() })
	if blank, err := system.IsBlank(ctx, mapper); err != nil || !blank {
		t.Fatalf("fresh mapper blank=%v err=%v, want blank", blank, err)
	}
	if err := system.MakeFilesystem(ctx, mapper, "ext4"); err != nil {
		t.Fatal(err)
	}
	mountpoint := filepath.Join(t.TempDir(), "work")
	if err := system.Mount(ctx, mapper, mountpoint, "ext4", []string{"discard", "noatime"}); err != nil {
		if errors.Is(err, unix.EPERM) {
			t.Skipf("mount not permitted here: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(mountpoint, unix.MNT_DETACH) })
	if err := system.Adopt(mountpoint); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("postflight-plaintext-sentinel")
	if err := os.WriteFile(filepath.Join(mountpoint, "sentinel"), sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	system.Sync()
	if err := system.Unmount(mountpoint); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("cryptsetup", "close", name).Run(); err != nil {
		t.Fatal(err)
	}

	// The raw device must never contain the sentinel plaintext.
	raw, err := os.ReadFile(device)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, sentinel) {
		t.Fatal("sentinel plaintext visible on the raw device")
	}

	// Reopen with the same key: the filesystem is already there.
	mapper, err = system.OpenLUKS(ctx, device, name, key)
	if err != nil {
		t.Fatal(err)
	}
	if blank, err := system.IsBlank(ctx, mapper); err != nil || blank {
		t.Fatalf("reopened mapper blank=%v err=%v, want filesystem", blank, err)
	}
}

func TestRealSystemRunnerHomeOverlay(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("overlay mount conformance needs root")
	}
	root := t.TempDir()
	lower := filepath.Join(root, "image-home")
	lowerBind := filepath.Join(root, "image-home-lower")
	backing := filepath.Join(root, "durable")
	upper := filepath.Join(backing, "upper")
	work := filepath.Join(backing, "work")
	target := filepath.Join(root, "runner-home")
	for _, directory := range []string{lower, backing, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(lower, "cargo"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	system := RealSystem{RunnerUser: "root"}
	if err := system.MountOverlay(context.Background(), lower, lowerBind, upper, work, target, []string{"noatime", "nodev", "nosuid"}); err != nil {
		if errors.Is(err, unix.EPERM) {
			t.Skipf("overlay mount not permitted here: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Unmount(target, unix.MNT_DETACH)
		_ = unix.Unmount(lowerBind, unix.MNT_DETACH)
	})
	if err := system.MountOverlay(context.Background(), lower, lowerBind, upper, work, target, nil); err != nil {
		t.Fatalf("idempotent overlay mount: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(target, "cargo")); err != nil || string(raw) != "image" {
		t.Fatalf("image lower file = %q, %v", raw, err)
	}
	if err := os.WriteFile(filepath.Join(target, "cache"), []byte("durable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(upper, "cache")); err != nil || string(raw) != "durable" {
		t.Fatalf("durable upper file = %q, %v", raw, err)
	}
	if err := os.WriteFile(filepath.Join(lowerBind, "forbidden"), nil, 0o644); err == nil {
		t.Fatal("read-only image lower accepted a write")
	}
}
