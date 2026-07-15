package vm

import (
	"path/filepath"
	"strconv"
)

// machineType is the pinned guest platform. A versioned q35 keeps the device
// model identical across QEMU upgrades: argv determinism is load-bearing —
// the same class must produce byte-identical argv on every host, because the
// launch configuration feeds attestation measurement stability.
const machineType = "pc-q35-8.2"

// workspaceNode names the hot-attached workspace on both sides of the QMP
// seam: the blockdev node, the qdev id (dev- prefixed), and the SCSI serial
// the guest mounts by (/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_workspace).
const workspaceNode = "workspace"

// workspaceDevice is the qdev id of the workspace's scsi-hd.
const workspaceDevice = "dev-" + workspaceNode

// LaunchSpec is everything that determines one VM's QEMU invocation.
type LaunchSpec struct {
	QEMUPath   string
	ID         ID
	CPUs       int
	MemoryMiB  int
	RootDevice string
	StateDir   string
	VsockCID   uint32
}

func qmpSocketPath(stateDir string) string { return filepath.Join(stateDir, "qmp.sock") }
func serialLogPath(stateDir string) string { return filepath.Join(stateDir, "serial.log") }

// Argv renders the deterministic QEMU invocation for this spec. The shape is
// the tracer-proven recipe: virtio-scsi controller provisioned at boot
// (hot-attach requires it), root disk as a raw host_device with direct IO,
// QMP on a per-VM unix socket, serial console to a file, seccomp sandbox on,
// and a per-VM vsock CID for the guestd channel. Process supervision flags
// (-daemonize, -pidfile) are deliberately absent: the Launcher owns the
// process lifetime.
func (s LaunchSpec) Argv() []string {
	return []string{
		s.QEMUPath,
		"-nodefaults",
		"-machine", machineType + ",accel=kvm",
		"-cpu", "host",
		"-smp", strconv.Itoa(s.CPUs),
		"-m", strconv.Itoa(s.MemoryMiB),
		"-name", "postflight-vm-" + string(s.ID),
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-display", "none",
		"-serial", "file:" + serialLogPath(s.StateDir),
		"-qmp", "unix:" + qmpSocketPath(s.StateDir) + ",server=on,wait=off",
		"-device", "virtio-scsi-pci,id=scsi0",
		"-blockdev", "driver=raw,node-name=root,file.driver=host_device,file.filename=" + s.RootDevice + ",file.cache.direct=on,file.aio=native",
		"-device", "scsi-hd,bus=scsi0.0,drive=root,serial=root,bootindex=0",
		"-device", "virtio-rng-pci",
		"-device", "vhost-vsock-pci,guest-cid=" + strconv.FormatUint(uint64(s.VsockCID), 10),
	}
}
