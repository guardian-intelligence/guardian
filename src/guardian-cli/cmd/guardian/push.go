package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// component pairs a Bazel-built OCI layout (riding in the guardian binary's
// runfiles) with the manifest template it feeds. Workload image references
// are computed as registry.guardian.internal/<name>@<built digest>: what runs
// is byte-for-byte what the workspace built.
type component struct {
	name     string
	layout   string // runfiles path of the rules_oci OCI layout directory
	manifest string // repo-root-relative manifest template
}

var components = []component{{
	name:     "openbao",
	layout:   "_main/src/infrastructure-components/openbao/image",
	manifest: "src/infrastructure-components/openbao/k8s/openbao.yaml.tmpl",
}, {
	// postgres before aisucks: the service retries its DB connection, but
	// there is no reason to start the race with the database behind.
	name:     "postgres",
	layout:   "_main/src/infrastructure-components/postgres/image",
	manifest: "src/infrastructure-components/postgres/k8s/postgres.yaml.tmpl",
}, {
	name:     "aisucks",
	layout:   "_main/src/aisucks/image",
	manifest: "src/aisucks/k8s/aisucks.yaml.tmpl",
}, {
	name:     "gatus",
	layout:   "_main/src/infrastructure-components/gatus/image",
	manifest: "src/infrastructure-components/gatus/k8s/gatus.yaml.tmpl",
}, {
	// victoria-metrics first among observability components: its manifest
	// owns the observability Namespace, which the rest of the stack
	// assumes already exists.
	name:     "victoria-metrics",
	layout:   "_main/src/infrastructure-components/victoria-metrics/image",
	manifest: "src/infrastructure-components/victoria-metrics/k8s/victoria-metrics.yaml.tmpl",
}, {
	name:     "kube-state-metrics",
	layout:   "_main/src/infrastructure-components/kube-state-metrics/image",
	manifest: "src/infrastructure-components/kube-state-metrics/k8s/kube-state-metrics.yaml.tmpl",
}, {
	name:     "otel-collector",
	layout:   "_main/src/infrastructure-components/otel-collector/image",
	manifest: "src/infrastructure-components/otel-collector/k8s/otel-collector.yaml.tmpl",
}, {
	// After victoria-metrics: the observability Namespace object is owned
	// by the victoria-metrics manifest and alertmanager assumes it exists.
	name:     "alertmanager",
	layout:   "_main/src/infrastructure-components/alertmanager/image",
	manifest: "src/infrastructure-components/alertmanager/k8s/alertmanager.yaml.tmpl",
}, {
	// vmalert after victoria-metrics (owns the observability Namespace and
	// is the datasource) and alertmanager (the notifier): it retries both,
	// but there is no reason to start rule eval with its backends behind.
	name:     "vmalert",
	layout:   "_main/src/infrastructure-components/vmalert/image",
	manifest: "src/infrastructure-components/vmalert/k8s/vmalert.yaml.tmpl",
}, {
	name:     "blackbox-exporter",
	layout:   "_main/src/infrastructure-components/blackbox-exporter/image",
	manifest: "src/infrastructure-components/blackbox-exporter/k8s/blackbox-exporter.yaml.tmpl",
}, {
	// grafana after victoria-metrics: the observability Namespace object is
	// owned by the victoria-metrics manifest, and the datasource points at
	// the VM service.
	name:     "grafana",
	layout:   "_main/src/infrastructure-components/grafana/image",
	manifest: "src/infrastructure-components/grafana/k8s/grafana.yaml.tmpl",
}}

// seedRegistryManifest is substrate applied by `up` before any component:
// the in-cluster registry that workspace artifacts are pushed through.
// (The CNI sits even lower and guardian deliberately knows nothing about
// it: Cilium ships as a Talos inlineManifests patch — see
// src/infrastructure-components/cilium/ — applied by Talos's own bootstrap
// manifest controller, the same mechanism that deployed flannel.)
const (
	seedRegistryManifest = "src/infrastructure-components/seed-registry/k8s/seed-registry.yaml"
	mirrorHost           = "registry.guardian.internal"
	pushLocalPort        = 53000
)

// layoutDigest reads the single image manifest digest from an OCI layout.
func layoutDigest(dir string) (v1.Hash, error) {
	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		return v1.Hash{}, fmt.Errorf("oci layout %s: %w", dir, err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return v1.Hash{}, fmt.Errorf("oci layout %s: %w", dir, err)
	}
	if len(im.Manifests) != 1 {
		return v1.Hash{}, fmt.Errorf("oci layout %s: expected exactly one manifest, found %d", dir, len(im.Manifests))
	}
	return im.Manifests[0].Digest, nil
}

// pushLayout pushes the layout's image to <endpoint>/<repo> by digest and
// returns the digest. The endpoint is plain HTTP: pushes only ever travel a
// kubectl port-forward (or a test server) on loopback.
func pushLayout(dir, endpoint, repo string) (v1.Hash, error) {
	digest, err := layoutDigest(dir)
	if err != nil {
		return v1.Hash{}, err
	}
	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		return v1.Hash{}, fmt.Errorf("oci layout %s: %w", dir, err)
	}
	img, err := idx.Image(digest)
	if err != nil {
		return v1.Hash{}, fmt.Errorf("oci layout %s: %w", dir, err)
	}
	ref, err := name.NewDigest(fmt.Sprintf("%s/%s@%s", endpoint, repo, digest), name.Insecure)
	if err != nil {
		return v1.Hash{}, fmt.Errorf("push %s: %w", repo, err)
	}
	if err := remote.Write(ref, img); err != nil {
		return v1.Hash{}, fmt.Errorf("push %s to %s: %w", repo, endpoint, err)
	}
	return digest, nil
}

// withPortForward runs fn while a kubectl port-forward to namespace/target is
// up on localPort→remotePort, and tears the forward down afterwards. The
// endpoint handed to fn is plain-HTTP loopback: forwards only ever carry
// traffic on localhost. readyTimeout bounds the wait for the local socket —
// short for best-effort probes, generous for converge steps.
func withPortForward(kubectl, kubeconfig, namespace, target string, localPort, remotePort int, readyTimeout time.Duration, fn func(endpoint string) error) error {
	cmd := exec.Command(kubectl, "--kubeconfig", kubeconfig, "-n", namespace,
		"port-forward", target, fmt.Sprintf("%d:%d", localPort, remotePort))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("port-forward %s/%s: %w", namespace, target, err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	endpoint := fmt.Sprintf("127.0.0.1:%d", localPort)
	err := poll(fmt.Sprintf("port-forward %s/%s", namespace, target), readyTimeout, 2*time.Second, func() error {
		conn, derr := net.DialTimeout("tcp", endpoint, time.Second)
		if derr != nil {
			return derr
		}
		return conn.Close()
	})
	if err != nil {
		return err
	}
	return fn(endpoint)
}
