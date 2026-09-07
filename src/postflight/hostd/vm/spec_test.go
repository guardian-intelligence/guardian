package vm

import (
	"strings"
	"testing"
)

func TestTurboLaunchDoesNotRequireSNP(t *testing.T) {
	spec := LaunchSpec{Flavor: FlavorTurbo, QEMUPath: "/usr/bin/qemu-system-x86_64", ID: "turbo-1", CPUs: 4, MemoryMiB: 16384, Firmware: "/usr/share/seabios/bios.bin", VsockCID: 4}
	argv := strings.Join(spec.Argv(), " ")
	for _, forbidden := range []string{"sev", "confidential-guest", "EPYC-v4", "pc-q35-11.0"} {
		if strings.Contains(argv, forbidden) {
			t.Fatalf("Turbo launch includes %s: %s", forbidden, argv)
		}
	}
	for _, required := range []string{"pc-q35-8.2,accel=kvm", "-cpu host", "vhost-vsock-pci,guest-cid=4", "virtio-scsi-pci"} {
		if !strings.Contains(argv, required) {
			t.Fatalf("Turbo launch lacks %s: %s", required, argv)
		}
	}
}

func TestTurboPrivilegeDropRemainsPossibleUnderSeccomp(t *testing.T) {
	argv := strings.Join((LaunchSpec{Flavor: FlavorTurbo, RunAs: "postflight-vm"}).Argv(), " ")
	if !strings.Contains(argv, "-runas postflight-vm") ||
		!strings.Contains(argv, "-sandbox on,obsolete=deny,elevateprivileges=allow,spawn=deny,resourcecontrol=deny") {
		t.Fatal("Turbo must permit its initial UID drop while retaining the remaining syscall filters")
	}
	for _, spec := range []LaunchSpec{{Flavor: FlavorTurbo}, {Flavor: FlavorConfidential}} {
		if !strings.Contains(strings.Join(spec.Argv(), " "), "elevateprivileges=deny") {
			t.Fatal("launches without a Turbo UID drop must retain the credential syscall filter")
		}
	}
}

func TestRunnerClassFlavorIsClosed(t *testing.T) {
	for class, want := range map[Class]Flavor{TurboClass: FlavorTurbo, ConfidentialClass: FlavorConfidential} {
		got, err := ClassFlavor(class)
		if err != nil || got != want {
			t.Fatalf("ClassFlavor(%q) = %q, %v", class, got, err)
		}
	}
	for _, class := range []Class{"", "postflight-4vcpu-ubuntu24", "postflight-4vcpu-ubuntu24-tubro"} {
		if _, err := ClassFlavor(class); err == nil {
			t.Fatalf("accepted ambiguous class %q", class)
		}
	}
}

// TestArgvGolden pins the exact QEMU invocation for a fixture class. Argv
// determinism is load-bearing (attestation measurement stability rides on
// it), so any change here is a deliberate platform revision, not a refactor.
func TestArgvGolden(t *testing.T) {
	spec := LaunchSpec{
		QEMUPath:   "/usr/bin/qemu-system-x86_64",
		ID:         "pool-0001",
		CPUs:       4,
		MemoryMiB:  16384,
		RootDevice: "/dev/zvol/tank/postflight/vm-pool-0001",
		Firmware:   "/usr/share/postflight/OVMF.fd",
		StateDir:   "/var/lib/hostd/vms/pool-0001",
		VsockCID:   3,
	}
	golden := strings.Join([]string{
		"/usr/bin/qemu-system-x86_64",
		"-nodefaults",
		"-machine", "pc-q35-11.0,accel=kvm,confidential-guest-support=sev0",
		"-object", "sev-snp-guest,id=sev0,cbitpos=51,reduced-phys-bits=1,policy=0x30000",
		"-cpu", "EPYC-v4",
		"-smp", "4",
		"-m", "16384",
		"-name", "postflight-vm-pool-0001",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-display", "none",
		"-serial", "file:/var/lib/hostd/vms/pool-0001/serial.log",
		"-qmp", "unix:/var/lib/hostd/vms/pool-0001/qmp.sock,server=on,wait=off",
		"-bios", "/usr/share/postflight/OVMF.fd",
		"-device", "virtio-scsi-pci,id=scsi0",
		"-blockdev", "driver=raw,node-name=root,file.driver=host_device,file.filename=/dev/zvol/tank/postflight/vm-pool-0001,file.cache.direct=on,file.aio=native",
		"-device", "scsi-hd,bus=scsi0.0,drive=root,serial=root,bootindex=0",
		"-device", "virtio-rng-pci",
		"-device", "vhost-vsock-pci,guest-cid=3",
	}, "\n")
	if got := strings.Join(spec.Argv(), "\n"); got != golden {
		t.Fatalf("argv drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
}

// TestArgvUserNetwork pins the two-arg libslirp NIC appended for the user
// egress datapath, and that it lands after the vsock device so the base
// shape is unchanged.
func TestArgvUserNetwork(t *testing.T) {
	spec := LaunchSpec{
		QEMUPath:     "/usr/bin/qemu-system-x86_64",
		ID:           "pool-0001",
		CPUs:         4,
		MemoryMiB:    16384,
		RootDevice:   "/dev/zvol/tank/postflight/vm-pool-0001",
		Firmware:     "/usr/share/postflight/OVMF.fd",
		StateDir:     "/var/lib/hostd/vms/pool-0001",
		VsockCID:     3,
		GuestNetwork: "user",
	}
	got := spec.Argv()
	tail := strings.Join(got[len(got)-4:], "\n")
	want := strings.Join([]string{
		"-netdev", "user,id=net0",
		"-device", "virtio-net-pci,netdev=net0",
	}, "\n")
	if tail != want {
		t.Fatalf("user NIC argv drifted:\n--- got ---\n%s\n--- want ---\n%s", tail, want)
	}
	// The NIC only appends: the base shape must be byte-identical to the
	// networkless argv, so a user-mode VM measures the same up to its NIC.
	base := spec
	base.GuestNetwork = "none"
	if head := strings.Join(got[:len(got)-4], "\n"); head != strings.Join(base.Argv(), "\n") {
		t.Fatalf("user NIC changed the base argv shape:\n%s", head)
	}
}

func TestArgvTapNetwork(t *testing.T) {
	spec := LaunchSpec{
		QEMUPath:     "/usr/bin/qemu-system-x86_64",
		ID:           "pool-0001",
		CPUs:         4,
		MemoryMiB:    16384,
		RootDevice:   "/dev/zvol/tank/postflight/vm-pool-0001",
		Firmware:     "/usr/share/postflight/OVMF.fd",
		StateDir:     "/var/lib/hostd/vms/pool-0001",
		VsockCID:     3,
		GuestNetwork: "tap",
	}
	got := spec.Argv()
	tail := strings.Join(got[len(got)-4:], "\n")
	want := strings.Join([]string{
		"-netdev", "tap,id=net0,ifname=pft22090518f15b,script=no,downscript=no,vhost=on",
		"-device", "virtio-net-pci,netdev=net0,mac=02:00:00:00:00:03",
	}, "\n")
	if tail != want {
		t.Fatalf("tap NIC argv drifted:\n--- got ---\n%s\n--- want ---\n%s", tail, want)
	}
	ifname, mac := tapIdentity(spec.ID, spec.VsockCID)
	if len(ifname) > 15 || ifname != "pft22090518f15b" || mac != "02:00:00:00:00:03" {
		t.Fatalf("tap identity ifname=%q mac=%q", ifname, mac)
	}
}

// TestArgvNoNetworkByDefault: an unset or none datapath attaches no NIC.
func TestArgvNoNetworkByDefault(t *testing.T) {
	for _, mode := range []string{"", "none"} {
		spec := LaunchSpec{QEMUPath: "q", ID: "x", CPUs: 1, MemoryMiB: 1, RootDevice: "d", Firmware: "f", StateDir: "s", VsockCID: 1, GuestNetwork: mode}
		if strings.Contains(strings.Join(spec.Argv(), " "), "netdev") {
			t.Fatalf("mode %q attached a NIC", mode)
		}
	}
}

// TestArgvDeterminism: same spec, same bytes, every time.
func TestArgvDeterminism(t *testing.T) {
	spec := LaunchSpec{
		QEMUPath:   "/usr/bin/qemu-system-x86_64",
		ID:         "pool-0002",
		CPUs:       2,
		MemoryMiB:  2048,
		RootDevice: "/dev/zvol/tank/postflight/vm-pool-0002",
		Firmware:   "/usr/share/postflight/OVMF.fd",
		StateDir:   "/var/lib/hostd/vms/pool-0002",
		VsockCID:   4,
	}
	first := argvDigest(spec.Argv())
	for i := 0; i < 8; i++ {
		if argvDigest(spec.Argv()) != first {
			t.Fatal("argv is not deterministic")
		}
	}
}
