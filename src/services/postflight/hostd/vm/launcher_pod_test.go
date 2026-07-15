package vm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"sigs.k8s.io/yaml"
)

type fakePod struct {
	labels map[string]string
	// gracePolls is how many Alive calls the pod survives after Delete,
	// simulating the asynchronous termination grace period. -1: not deleted.
	gracePolls int
}

type fakePodAPI struct {
	mu      sync.Mutex
	applied [][]byte
	pods    map[string]*fakePod
	deleted []string
	// deleteGrace is the gracePolls a Delete assigns.
	deleteGrace int
	aliveErr    error
}

func newFakePodAPI() *fakePodAPI { return &fakePodAPI{pods: map[string]*fakePod{}} }

func (a *fakePodAPI) Apply(_ context.Context, manifest []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var pod podManifest
	if err := yaml.Unmarshal(manifest, &pod); err != nil {
		return err
	}
	a.pods[pod.Metadata.Namespace+"/"+pod.Metadata.Name] = &fakePod{labels: pod.Metadata.Labels, gracePolls: -1}
	a.applied = append(a.applied, manifest)
	return nil
}

func (a *fakePodAPI) Alive(_ context.Context, namespace, name string) (bool, map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pod, ok := a.pods[namespace+"/"+name]
	if !ok {
		return false, nil, a.aliveErr
	}
	if pod.gracePolls >= 0 {
		if pod.gracePolls == 0 {
			delete(a.pods, namespace+"/"+name)
			return false, nil, a.aliveErr
		}
		pod.gracePolls--
	}
	return true, pod.labels, a.aliveErr
}

func (a *fakePodAPI) Delete(_ context.Context, namespace, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if pod, ok := a.pods[namespace+"/"+name]; ok {
		pod.gracePolls = a.deleteGrace
	}
	a.deleted = append(a.deleted, namespace+"/"+name)
	return nil
}

func (a *fakePodAPI) podAlive(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	pod, ok := a.pods[name]
	return ok && pod.gracePolls != 0
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
    postflight.guardianintelligence.org/argv-sha256: ea34a324a3e7daabbed84bfe6c2cba4d2b445ec32f81029cc1a12d43a7783991
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
	alive, err := launcher.Alive(ctx, "vm-a", "", argv)
	if err != nil || !alive {
		t.Fatalf("alive=%t err=%v", alive, err)
	}
	if err := launcher.Kill(ctx, "vm-a", "", argv); err != nil {
		t.Fatal(err)
	}
	alive, err = launcher.Alive(ctx, "vm-a", "", argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v after kill", alive, err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != "postflight-vms/postflight-vm-vm-a" {
		t.Fatalf("deleted %v", api.deleted)
	}
	// Kill on an absent pod is idempotent and issues no further deletes.
	if err := launcher.Kill(ctx, "vm-a", "", argv); err != nil {
		t.Fatal(err)
	}
	if len(api.deleted) != 1 {
		t.Fatalf("deleted %v after idempotent kill", api.deleted)
	}
}

// TestPodLauncherKillWaitsForTermination pins the Launcher contract: Kill
// returns only once the pod is really gone. Pod deletion is asynchronous
// (default 30s grace), and returning early would let the dying QEMU hold
// the root zvol and its vsock CID past the driver's cleanup.
func TestPodLauncherKillWaitsForTermination(t *testing.T) {
	api := newFakePodAPI()
	api.deleteGrace = 3 // the pod survives three liveness polls after Delete
	launcher := &PodLauncher{API: api, Namespace: "postflight-vms", Image: "ghcr.io/guardian-intelligence/postflight-qemu@sha256:aaaa"}
	ctx := context.Background()
	argv := []string{"/usr/bin/qemu-system-x86_64", "-nodefaults"}
	if err := launcher.Start(ctx, "vm-a", "/var/lib/hostd/vms/vm-a", argv); err != nil {
		t.Fatal(err)
	}
	if err := launcher.Kill(ctx, "vm-a", "", argv); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if api.podAlive("postflight-vms/postflight-vm-vm-a") {
		t.Fatal("kill returned while the pod was still terminating")
	}
}

// TestPodLauncherNeverTouchesStrangers: a pod squatting on this VM's name
// but not carrying its identity labels is neither reported alive nor
// deleted — the pid-reuse guard's pod-shaped equivalent.
func TestPodLauncherNeverTouchesStrangers(t *testing.T) {
	api := newFakePodAPI()
	launcher := &PodLauncher{API: api, Namespace: "postflight-vms", Image: "ghcr.io/guardian-intelligence/postflight-qemu@sha256:aaaa"}
	ctx := context.Background()
	argv := []string{"/usr/bin/qemu-system-x86_64", "-nodefaults"}
	api.pods["postflight-vms/postflight-vm-vm-a"] = &fakePod{
		labels:     map[string]string{vmIDLabel: "vm-other", argvHashLabel: "not-our-argv"},
		gracePolls: -1,
	}
	alive, err := launcher.Alive(ctx, "vm-a", "", argv)
	if err != nil || alive {
		t.Fatalf("alive=%t err=%v for a stranger pod", alive, err)
	}
	if err := launcher.Kill(ctx, "vm-a", "", argv); err == nil {
		t.Fatal("kill accepted a stranger pod")
	}
	if len(api.deleted) != 0 {
		t.Fatalf("deleted %v; the stranger must survive", api.deleted)
	}
}
