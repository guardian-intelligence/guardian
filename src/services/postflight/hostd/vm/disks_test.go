package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWholeBlockDevicePinsDirectDisk(t *testing.T) {
	device, sysfs := blockDeviceFixture(t, false)
	got, err := resolveWholeBlockDevice(device, sysfs)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(filepath.Dir(device), "zd42")
	if got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
}

func TestResolveWholeBlockDeviceEscapesPartitionAlias(t *testing.T) {
	device, sysfs := blockDeviceFixture(t, true)
	got, err := resolveWholeBlockDevice(device, sysfs)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(filepath.Dir(device), "zd42")
	if got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
	if size, err := blockDeviceSize(got, sysfs); err != nil || size != 80*1024*1024*1024 {
		t.Fatalf("whole disk size=%d err=%v", size, err)
	}
}

func blockDeviceFixture(t *testing.T, partitionAlias bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	dev := filepath.Join(root, "dev")
	sysfs := filepath.Join(root, "sys", "class", "block")
	realDisk := filepath.Join(root, "sys", "devices", "virtual", "block", "zd42")
	realPartition := filepath.Join(realDisk, "zd42p16")
	for _, dir := range []string{dev, sysfs, realDisk, realPartition} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(realDisk, "size"), []byte("167772160\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realPartition, "size"), []byte("1869825\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realPartition, "partition"), []byte("16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"zd42": realDisk, "zd42p16": realPartition} {
		if err := os.Symlink(target, filepath.Join(sysfs, name)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := "zd42"
	if partitionAlias {
		target = "zd42p16"
	}
	device := filepath.Join(dev, "root")
	if err := os.Symlink(target, device); err != nil {
		t.Fatal(err)
	}
	return device, sysfs
}
