package vm

import (
	"context"
	"fmt"

	"sigs.k8s.io/yaml"
)

// PodAPI is the sliver of the kubelet's apiserver PodLauncher needs.
type PodAPI interface {
	// Apply submits a rendered pod manifest.
	Apply(ctx context.Context, manifest []byte) error
	// Alive reports whether the named pod exists and has not terminated.
	Alive(ctx context.Context, namespace, name string) (bool, error)
	// Delete removes the named pod; deleting an absent pod succeeds.
	Delete(ctx context.Context, namespace, name string) error
}

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
				"app.kubernetes.io/name":                    "postflight-vm",
				"postflight.guardianintelligence.org/vm-id": string(id),
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

// Alive implements Launcher.
func (l *PodLauncher) Alive(ctx context.Context, id ID, _ string, _ []string) (bool, error) {
	return l.API.Alive(ctx, l.Namespace, podName(id))
}

// Kill implements Launcher.
func (l *PodLauncher) Kill(ctx context.Context, id ID, _ string, _ []string) error {
	return l.API.Delete(ctx, l.Namespace, podName(id))
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
