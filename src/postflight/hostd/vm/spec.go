package vm

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strconv"
)

// MachineType is the pinned guest platform. A versioned q35 keeps the device
// model identical across QEMU upgrades: argv determinism is load-bearing —
// the same class must produce byte-identical argv on every host, because the
// launch configuration feeds attestation measurement stability.
const MachineType = "pc-q35-11.0"

// CPUModel is the portable SNP CPU contract for the first confidential
// class. "host" exposes topology leaves that AMD firmware can reject during
// SNP launch and also makes a process checkpoint hostage to the donor's
// exact microarchitecture.
const CPUModel = "EPYC-v4"

const (
	sevSNPObject = "sev-snp-guest,id=sev0,cbitpos=51,reduced-phys-bits=1,policy=0x30000"
	sevSNPPolicy = uint64(0x30000)
)

// workspaceNode names the hot-attached workspace on both sides of the QMP
// seam: the blockdev node, the qdev id (dev- prefixed), and the SCSI serial
// the guest mounts by (guestproto.DiskByIDPrefix + workspaceNode).
const workspaceNode = "workspace"

const processNode = "process"

const toolNode = "tool"

// workspaceDevice is the qdev id of the workspace's scsi-hd.
const workspaceDevice = "dev-" + workspaceNode

const processDevice = "dev-" + processNode

const toolDevice = "dev-" + toolNode

// workspaceFilesystem is what the guest creates on a blank workspace zvol.
const workspaceFilesystem = "ext4"

// guestNetworkUser is the LaunchSpec.GuestNetwork value selecting a
// libslirp user-mode NIC.
const guestNetworkUser = "user"

// guestNetworkTap is the production egress datapath. Hostd creates one
// short-lived tap per VM before QEMU starts and attaches it to the host's
// filtered bridge. QEMU only opens the prepared interface.
const guestNetworkTap = "tap"

// workspaceMountOptions shape the guest-side mount. discard is load-bearing:
// TRIM must pass through to the sparse zvol or NVMe accounting measures
// garbage retention.
var workspaceMountOptions = []string{"discard", "noatime", "nodev", "nosuid"}

var processMountOptions = []string{"discard", "noatime", "nodev", "noexec", "nosuid"}

var toolMountOptions = []string{"discard", "noatime", "nodev", "nosuid"}

const (
	processMountpoint = "/var/lib/postflight/process"
	processImagesDir  = processMountpoint + "/images"
	// The tool generation owns the runner's full home, including language
	// caches and the _work tree reached through /opt/actions-runner/_work.
	// The repository workspace is a nested mount and must be mounted after
	// this parent during rendezvous.
	runnerStateMountpoint = "/home/runner"
)

// LaunchSpec is everything that determines one VM's QEMU invocation.
type LaunchSpec struct {
	QEMUPath   string
	ID         ID
	CPUs       int
	MemoryMiB  int
	RootDevice string
	// Firmware is the measured AmdSev OVMF.fd blob. It is supplied as one
	// firmware image because an SNP launch encrypts and measures the whole
	// boot blob before the first vCPU runs.
	Firmware string
	StateDir string
	VsockCID uint32
	// GuestNetwork selects the guest's egress datapath. "" or "none" attaches
	// no NIC (the guestd control channel is vsock, which needs none); "user"
	// attaches a libslirp user-mode NIC giving the runner NAT egress to GitHub
	// and reaching host services via the 10.0.2.2 gateway. The static shape
	// keeps argv deterministic. "tap" opens a vhost-accelerated interface that
	// hostd prepared before launching QEMU.
	GuestNetwork string
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
	argv := []string{
		s.QEMUPath,
		"-nodefaults",
		"-machine", MachineType + ",accel=kvm,confidential-guest-support=sev0",
		"-object", sevSNPObject,
		"-cpu", CPUModel,
		"-smp", strconv.Itoa(s.CPUs),
		"-m", strconv.Itoa(s.MemoryMiB),
		"-name", "postflight-vm-" + string(s.ID),
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-display", "none",
		"-serial", "file:" + serialLogPath(s.StateDir),
		"-qmp", "unix:" + qmpSocketPath(s.StateDir) + ",server=on,wait=off",
		"-bios", s.Firmware,
		"-device", "virtio-scsi-pci,id=scsi0",
		"-blockdev", "driver=raw,node-name=root,file.driver=host_device,file.filename=" + s.RootDevice + ",file.cache.direct=on,file.aio=native",
		"-device", "scsi-hd,bus=scsi0.0,drive=root,serial=root,bootindex=0",
		"-device", "virtio-rng-pci",
		"-device", "vhost-vsock-pci,guest-cid=" + strconv.FormatUint(uint64(s.VsockCID), 10),
	}
	if s.GuestNetwork == guestNetworkUser {
		argv = append(argv,
			"-netdev", "user,id=net0",
			"-device", "virtio-net-pci,netdev=net0",
		)
	} else if s.GuestNetwork == guestNetworkTap {
		ifname, mac := tapIdentity(s.ID, s.VsockCID)
		argv = append(argv,
			"-netdev", "tap,id=net0,ifname="+ifname+",script=no,downscript=no,vhost=on",
			"-device", "virtio-net-pci,netdev=net0,mac="+mac,
		)
	}
	return argv
}

func tapIdentity(id ID, cid uint32) (string, string) {
	digest := sha256.Sum256([]byte(id))
	ifname := fmt.Sprintf("pft%x", digest[:6])
	mac := fmt.Sprintf("02:00:%02x:%02x:%02x:%02x", byte(cid>>24), byte(cid>>16), byte(cid>>8), byte(cid))
	return ifname, mac
}
