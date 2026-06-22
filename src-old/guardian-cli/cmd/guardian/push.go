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
// runfiles) with the Kubernetes desired-state unit it feeds. Image references
// are computed as registry.guardian.internal/<name>@<built digest>: what runs
// is byte-for-byte what the workspace built.
type component struct {
	name string
	// layout is the runfiles path of the rules_oci OCI layout directory.
	// Empty means the component only applies Kubernetes state.
	layout string
	// kustomization is the repo-root-relative Kustomize root. It replaces
	// manifest for components whose Kubernetes state is plain YAML.
	kustomization string
	pushOnly      bool
	// images is for components that patch several image refs into one
	// Kustomize root, such as cert-manager. Keys are the seed-registry
	// repository names.
	images []componentImage
	// enabled gates the component per host; nil converges on every host.
	// A manifest-only component MUST be gated (TestComponentsTable pins
	// this): with no image and no host gate there would be nothing
	// deliberate about where its objects land.
	enabled func(*Host) bool
}

type componentImage struct {
	name   string
	layout string
}

var components = []component{{
	name:          "openbao",
	layout:        "_main/src/infrastructure-components/openbao/image",
	kustomization: "src/k8s/bootstrap/openbao/base",
}, {
	name:          "crossplane",
	layout:        "_main/src/infrastructure-components/crossplane/image",
	kustomization: "src/k8s/bootstrap/crossplane/base",
}, {
	name:          "cert-manager",
	kustomization: "src/k8s/bootstrap/cert-manager/base",
	enabled:       hostUsesPlatformTLS,
	images: []componentImage{{
		name:   "cert-manager-cainjector",
		layout: "_main/src/infrastructure-components/cert-manager/cainjector",
	}, {
		name:   "cert-manager-controller",
		layout: "_main/src/infrastructure-components/cert-manager/controller",
	}, {
		name:   "cert-manager-webhook",
		layout: "_main/src/infrastructure-components/cert-manager/webhook",
	}, {
		name:   "cert-manager-startupapicheck",
		layout: "_main/src/infrastructure-components/cert-manager/startupapicheck",
	}, {
		name:   "cert-manager-acmesolver",
		layout: "_main/src/infrastructure-components/cert-manager/acmesolver",
	}},
}, {
	name:          "provider-kubernetes",
	kustomization: "src/k8s/bootstrap/provider-kubernetes/package",
	enabled:       hostUsesCrossplane,
}, {
	name:          "provider-kubernetes-config",
	kustomization: "src/k8s/bootstrap/provider-kubernetes/config",
	enabled:       hostUsesCrossplane,
}, {
	name:          "guardian-platform",
	kustomization: "src/crossplane/packages/guardian-platform",
	enabled:       hostUsesCrossplane,
}, {
	name:          "guardian-products",
	kustomization: "src/crossplane/packages/guardian-products",
	enabled:       hostUsesCrossplane,
}, {
	name:     "aisucks",
	layout:   "_main/src/products/aisucks/services/api/image",
	pushOnly: true,
}, {
	name:     "postgres",
	layout:   "_main/src/infrastructure-components/postgres/image",
	pushOnly: true,
}, {
	name:          "local-storage-bootstrap",
	kustomization: "src/k8s/bootstrap/local-storage/base",
	enabled:       hostUsesLocalStorage,
}, {
	name:     "directus",
	layout:   "_main/src/infrastructure-components/directus/image",
	pushOnly: true,
}, {
	name:          "gatus",
	layout:        "_main/src/infrastructure-components/gatus/image",
	kustomization: "src/k8s/reconciled/observability/gatus/base",
}, {
	// ObservabilityStack consumes this image from the environment bundle and
	// owns the observability Namespace/Deployment/Service through Crossplane.
	name:     "victoria-metrics",
	layout:   "_main/src/infrastructure-components/victoria-metrics/image",
	pushOnly: true,
}, {
	// Cluster-wide controller for SecretProjection's namespace-scoped
	// SecretStores and ExternalSecrets. Source access remains bounded by
	// per-projection OpenBao policies and Kubernetes auth roles.
	name:          "external-secrets",
	layout:        "_main/src/infrastructure-components/external-secrets/image",
	kustomization: "src/k8s/bootstrap/external-secrets/base",
}, {
	name:          "kube-state-metrics",
	layout:        "_main/src/infrastructure-components/kube-state-metrics/image",
	kustomization: "src/k8s/reconciled/observability/kube-state-metrics/base",
}, {
	// clickhouse after ObservabilityStack (the observability Namespace and
	// VictoriaMetrics owner) and before otel-collector: the collector's
	// clickhouse exporter retries, but there is no reason to start the logs
	// pipeline with its ledger behind. List order is apply order and no test
	// pins this pair — this comment carries the invariant. Host-gated like
	// gateway (the ledger ratchet: dev → gamma → prod); the otel-collector
	// config patches branch on the same flag, so a non-ledger environment's collector
	// stays byte-identical to the metrics-only spine. The otel schema is
	// NOT applied here: docs/runbooks/ledger.md applies clickhouse/ddl/ by hand
	// and the exporter runs create_schema: false.
	name:          "clickhouse",
	layout:        "_main/src/infrastructure-components/clickhouse/image",
	kustomization: "src/k8s/reconciled/observability/clickhouse/base",
	enabled:       func(s *Host) bool { return s.Clickhouse.Enabled },
}, {
	name:          "otel-collector",
	layout:        "_main/src/infrastructure-components/otel-collector/image",
	kustomization: "src/k8s/reconciled/observability/otel-collector/base",
}, {
	// After ObservabilityStack: alertmanager assumes the observability
	// Namespace and VictoriaMetrics service already exist.
	name:          "alertmanager",
	layout:        "_main/src/infrastructure-components/alertmanager/image",
	kustomization: "src/k8s/reconciled/observability/alertmanager/base",
}, {
	// vmalert after ObservabilityStack (owns the observability Namespace and
	// VictoriaMetrics datasource) and alertmanager (the notifier): it retries
	// both, but there is no reason to start rule eval with its backends
	// behind.
	name:          "vmalert",
	layout:        "_main/src/infrastructure-components/vmalert/image",
	kustomization: "src/k8s/reconciled/observability/vmalert/base",
}, {
	name:          "blackbox-exporter",
	layout:        "_main/src/infrastructure-components/blackbox-exporter/image",
	kustomization: "src/k8s/reconciled/observability/blackbox-exporter/base",
}, {
	// grafana after ObservabilityStack: the observability Namespace object is
	// owned by Crossplane, and the datasource points at the VM service.
	name:          "grafana",
	layout:        "_main/src/infrastructure-components/grafana/image",
	kustomization: "src/k8s/reconciled/observability/grafana/base",
}, {
	// StatusSurface consumes this image from the environment bundle and owns
	// the status Namespace/Deployment/Service/TLSRoute objects through
	// Crossplane.
	name:     "status",
	layout:   "_main/src/status/image",
	pushOnly: true,
}, {
	name:     "zot",
	layout:   "_main/src/infrastructure-components/zot/image",
	pushOnly: true,
	enabled:  hostUsesPlatformOCI,
}}

func hostUsesEdgeGateway(s *Host) bool {
	return s.Gateway.Enabled
}

func hostUsesCrossplane(*Host) bool {
	return true
}

func hostUsesLocalStorage(s *Host) bool {
	return s.Storage.ProductPool.Name != ""
}

func hostUsesPlatformTLS(s *Host) bool {
	return s.OCI.Domain != ""
}

func hostUsesPlatformOCI(s *Host) bool {
	return s.OCI.Domain != ""
}

func (c component) imageLayouts() []componentImage {
	out := make([]componentImage, 0, 1+len(c.images))
	if c.layout != "" {
		out = append(out, componentImage{name: c.name, layout: c.layout})
	}
	out = append(out, c.images...)
	return out
}

// seedRegistryKustomization is substrate applied by `up` before any component:
// the in-cluster registry that workspace artifacts are pushed through.
// (The CNI sits even lower and guardian deliberately knows nothing about
// it: Cilium ships as a Talos inlineManifests patch — see
// src/infrastructure-components/cilium/ — applied by Talos's own bootstrap
// manifest controller, the same mechanism that deployed flannel.)
const (
	seedRegistryKustomization = "src/k8s/bootstrap/seed-registry/base"
	mirrorHost                = "registry.guardian.internal"
	pushLocalPort             = 53000
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
// traffic on localhost. readyTimeout bounds the total wait for a usable local
// socket; short-lived port-forward failures are retried inside that budget.
func withPortForward(kubectl, kubeconfig, namespace, target string, localPort, remotePort int, readyTimeout time.Duration, fn func(endpoint string) error) error {
	endpoint := fmt.Sprintf("127.0.0.1:%d", localPort)
	deadline := time.Now().Add(readyTimeout)
	var lastErr error
	for {
		if remaining := time.Until(deadline); remaining <= 0 {
			return fmt.Errorf("timed out after %s waiting for port-forward %s/%s: %v", readyTimeout, namespace, target, lastErr)
		}
		cmd := exec.Command(kubectl, "--kubeconfig", kubeconfig, "-n", namespace,
			"port-forward", target, fmt.Sprintf("%d:%d", localPort, remotePort))
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			lastErr = fmt.Errorf("start port-forward: %w", err)
			fmt.Fprintf(os.Stderr, "waiting for port-forward %s/%s (retry in 2s)\n", namespace, target)
			sleepUntil(deadline, 2*time.Second)
			continue
		}
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()
		if err := waitPortForwardEndpoint(endpoint, deadline, done); err == nil {
			defer stopPortForward(cmd, done)
			return fn(endpoint)
		} else {
			lastErr = err
			stopPortForward(cmd, done)
			fmt.Fprintf(os.Stderr, "waiting for port-forward %s/%s (retry in 2s)\n", namespace, target)
			sleepUntil(deadline, 2*time.Second)
		}
	}
}

func waitPortForwardEndpoint(endpoint string, deadline time.Time, done <-chan error) error {
	var lastErr error
	for {
		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("port-forward exited before ready: %w", err)
			}
			return fmt.Errorf("port-forward exited before ready")
		default:
		}
		conn, err := net.DialTimeout("tcp", endpoint, time.Second)
		if err == nil {
			return conn.Close()
		}
		lastErr = err
		if time.Until(deadline) <= 0 {
			return lastErr
		}
		sleepUntil(deadline, 500*time.Millisecond)
	}
}

func stopPortForward(cmd *exec.Cmd, done <-chan error) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func sleepUntil(deadline time.Time, d time.Duration) {
	if remaining := time.Until(deadline); remaining < d {
		d = remaining
	}
	if d > 0 {
		time.Sleep(d)
	}
}
