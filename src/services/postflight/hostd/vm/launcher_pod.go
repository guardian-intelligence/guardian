package vm

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/yaml"
)

// PodAPI is the sliver of the kubelet's apiserver PodLauncher needs.
type PodAPI interface {
	// Apply submits a rendered pod manifest.
	Apply(ctx context.Context, manifest []byte) error
	// Alive reports whether the named pod exists and has not terminated,
	// along with its labels (nil when absent) for identity checks.
	Alive(ctx context.Context, namespace, name string) (bool, map[string]string, error)
	// Delete asks for the named pod's removal and returns once the deletion
	// is accepted, not once the pod is gone; deleting an absent pod
	// succeeds.
	Delete(ctx context.Context, namespace, name string) error
}

const (
	vmIDLabel     = "postflight.guardianintelligence.org/vm-id"
	argvHashLabel = "postflight.guardianintelligence.org/argv-sha256"
)

// PodLauncher runs each VM as its own pod on the Host's single-node
// cluster, which is what makes VM lifetime independent of hostd. The pod is
// pinned to restartPolicy Never: a restarted QEMU would be a resurrected
// sandbox, and destroy-and-refill forbids resurrection.
type PodLauncher struct {
	API PodAPI
	// Namespace holds the VM pods; it must be labeled for privileged pod
	// security.
	Namespace string
	// Image is the digest-pinned QEMU image ref.
	Image string
}

func podName(id ID) string { return "postflight-vm-" + string(id) }

// Manifest renders the pod for one VM. hostPath /dev rather than per-device
// mounts: zvol paths are symlinks into the shared devtmpfs, and workspace
// zvols cloned after the pod starts must appear inside it — mounting the
// whole devtmpfs is the tracer-proven recipe for both.
func (l *PodLauncher) Manifest(id ID, stateDir string, argv []string) ([]byte, error) {
	manifest := podManifest{
		APIVersion: "v1",
		Kind:       "Pod",
		Metadata: podMetadata{
			Name:      podName(id),
			Namespace: l.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": "postflight-vm",
				vmIDLabel:                string(id),
				argvHashLabel:            argvDigest(argv),
			},
		},
		Spec: podSpec{
			RestartPolicy: "Never",
			Containers: []podContainer{{
				Name:            "qemu",
				Image:           l.Image,
				Command:         argv,
				SecurityContext: &podSecurityContext{Privileged: true},
				VolumeMounts: []podVolumeMount{
					{Name: "dev", MountPath: "/dev"},
					{Name: "state", MountPath: stateDir},
				},
			}},
			Volumes: []podVolume{
				{Name: "dev", HostPath: podHostPath{Path: "/dev", Type: "Directory"}},
				{Name: "state", HostPath: podHostPath{Path: stateDir, Type: "Directory"}},
			},
		},
	}
	rendered, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("vm: rendering pod manifest: %w", err)
	}
	return rendered, nil
}

// Start implements Launcher.
func (l *PodLauncher) Start(ctx context.Context, id ID, stateDir string, argv []string) error {
	manifest, err := l.Manifest(id, stateDir, argv)
	if err != nil {
		return err
	}
	return l.API.Apply(ctx, manifest)
}

// podOwns reports whether a live pod's labels identify it as this VM's
// QEMU: the argv digest ties the pod to the exact invocation, so a stranger
// squatting on the postflight-vm-<id> name is never adopted or killed.
func podOwns(id ID, argv []string, labels map[string]string) bool {
	return labels[vmIDLabel] == string(id) && labels[argvHashLabel] == argvDigest(argv)
}

// Alive implements Launcher.
func (l *PodLauncher) Alive(ctx context.Context, id ID, _ string, argv []string) (bool, error) {
	alive, labels, err := l.API.Alive(ctx, l.Namespace, podName(id))
	if err != nil || !alive {
		return false, err
	}
	return podOwns(id, argv, labels), nil
}

// podKillWait bounds Kill's wait for the pod to really disappear: deletion
// is asynchronous with a default 30s grace period, and returning while the
// dying QEMU still holds the root zvol (and its vsock CID) would let the
// driver's cleanup race it.
const podKillWait = 60 * time.Second

// Kill implements Launcher.
func (l *PodLauncher) Kill(ctx context.Context, id ID, _ string, argv []string) error {
	name := podName(id)
	alive, labels, err := l.API.Alive(ctx, l.Namespace, name)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	if !podOwns(id, argv, labels) {
		return fmt.Errorf("vm: pod %s/%s is not vm %s's qemu; refusing to delete", l.Namespace, name, id)
	}
	if err := l.API.Delete(ctx, l.Namespace, name); err != nil {
		return err
	}
	deadline := time.Now().Add(podKillWait)
	for {
		alive, _, err := l.API.Alive(ctx, l.Namespace, name)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vm: pod %s/%s still terminating after %s", l.Namespace, name, podKillWait)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

type podManifest struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   podMetadata `json:"metadata"`
	Spec       podSpec     `json:"spec"`
}

type podMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type podSpec struct {
	RestartPolicy string         `json:"restartPolicy"`
	Containers    []podContainer `json:"containers"`
	Volumes       []podVolume    `json:"volumes,omitempty"`
}

type podContainer struct {
	Name            string              `json:"name"`
	Image           string              `json:"image"`
	Command         []string            `json:"command"`
	SecurityContext *podSecurityContext `json:"securityContext,omitempty"`
	VolumeMounts    []podVolumeMount    `json:"volumeMounts,omitempty"`
}

type podSecurityContext struct {
	Privileged bool `json:"privileged"`
}

type podVolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type podVolume struct {
	Name     string      `json:"name"`
	HostPath podHostPath `json:"hostPath"`
}

type podHostPath struct {
	Path string `json:"path"`
	Type string `json:"type"`
}
