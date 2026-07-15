package vm

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type fakePodAPI struct {
	mu       sync.Mutex
	applied  [][]byte
	pods     map[string]bool
	deleted  []string
	aliveErr error
}

func newFakePodAPI() *fakePodAPI { return &fakePodAPI{pods: map[string]bool{}} }

func (a *fakePodAPI) Apply(_ context.Context, manifest []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applied = append(a.applied, manifest)
	return nil
}

func (a *fakePodAPI) Alive(_ context.Context, namespace, name string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pods[namespace+"/"+name], a.aliveErr
}

func (a *fakePodAPI) Delete(_ context.Context, namespace, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pods, namespace+"/"+name)
	a.deleted = append(a.deleted, namespace+"/"+name)
	return nil
}

// TestPodManifestGolden pins the rendered pod for a fixture VM. restartPolicy
// Never is doctrine (a restarted QEMU is a resurrected sandbox), so this
// test failing means the launch contract changed, not that the golden needs
// a casual refresh.
func TestPodManifestGolden(t *testing.T) {
	launcher := &PodLauncher{
		Namespace: "postflight-vms",
		Image:     "ghcr.io/guardian-intelligence/postflight-qemu@sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	spec := LaunchSpec{
		QEMUPath:   "/usr/bin/qemu-system-x86_64",
		ID:         "pool-0001",
		CPUs:       4,
		MemoryMiB:  16384,
		RootDevice: "/dev/zvol/tank/postflight/vm-pool-0001",
		StateDir:   "/var/lib/hostd/vms/pool-0001",
		VsockCID:   3,
	}
	manifest, err := launcher.Manifest("pool-0001", spec.StateDir, spec.Argv())
	if err != nil {
		t.Fatal(err)
	}
	golden := `apiVersion: v1
kind: Pod
metadata:
  labels:
    app.kubernetes.io/name: postflight-vm
    postflight.guardianintelligence.org/vm-id: pool-0001
  name: postflight-vm-pool-0001
  namespace: postflight-vms
spec:
  containers:
  - command:
    - /usr/bin/qemu-system-x86_64
    - -nodefaults
    - -machine
    - pc-q35-8.2,accel=kvm
    - -cpu
    - host
    - -smp
    - "4"
    - -m
    - "16384"
    - -name
    - postflight-vm-pool-0001
    - -sandbox
    - on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny
    - -display
    - none
    - -serial
    - file:/var/lib/hostd/vms/pool-0001/serial.log
    - -qmp
    - unix:/var/lib/hostd/vms/pool-0001/qmp.sock,server=on,wait=off
    - -device
    - virtio-scsi-pci,id=scsi0
    - -blockdev
    - driver=raw,node-name=root,file.driver=host_device,file.filename=/dev/zvol/tank/postflight/vm-pool-0001,file.cache.direct=on,file.aio=native
    - -device
    - scsi-hd,bus=scsi0.0,drive=root,serial=root,bootindex=0
    - -device
    - virtio-rng-pci
    - -device
    - vhost-vsock-pci,guest-cid=3
    image: ghcr.io/guardian-intelligence/postflight-qemu@sha256:0000000000000000000000000000000000000000000000000000000000000000
    name: qemu
    securityContext:
      privileged: true
    volumeMounts:
    - mountPath: /dev
      name: dev
    - mountPath: /var/lib/hostd/vms/pool-0001
      name: state
  restartPolicy: Never
  volumes:
  - hostPath:
      path: /dev
      type: Directory
    name: dev
  - hostPath:
      path: /var/lib/hostd/vms/pool-0001
      type: Directory
    name: state
`
	if string(manifest) != golden {
		t.Fatalf("pod manifest drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", manifest, golden)
	}
}

func TestPodLauncherDelegates(t *testing.T) {
	api := newFakePodAPI()
	launcher := &PodLauncher{API: api, Namespace: "postflight-vms", Image: "ghcr.io/guardian-intelligence/postflight-qemu@sha256:aaaa"}
	ctx := context.Background()
	argv := []string{"/usr/bin/qemu-system-x86_64", "-nodefaults"}
	if err := launcher.Start(ctx, "vm-a", "/var/lib/hostd/vms/vm-a", argv); err != nil {
		t.Fatal(err)
	}
	if len(api.applied) != 1 || !strings.Contains(string(api.applied[0]), "postflight-vm-vm-a") {
		t.Fatalf("applied %d manifests: %s", len(api.applied), api.applied)
	}
	api.pods["postflight-vms/postflight-vm-vm-a"] = true
	alive, err := launcher.Alive(ctx, "vm-a", "", nil)
	if err != nil || !alive {
		t.Fatalf("alive=%t err=%v", alive, err)
	}
	if err := launcher.Kill(ctx, "vm-a", "", nil); err != nil {
		t.Fatal(err)
	}
	alive, err = launcher.Alive(ctx, "vm-a", "", nil)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v after kill", alive, err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != "postflight-vms/postflight-vm-vm-a" {
		t.Fatalf("deleted %v", api.deleted)
	}
}
