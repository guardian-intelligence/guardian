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
// is byte-for-byte what the workspace built. Zero values of the two optional
// fields preserve the default behavior: an image is pushed and the manifest
// converges on every site.
type component struct {
	name string
	// layout is the runfiles path of the rules_oci OCI layout directory.
	// Empty means manifest-only: nothing to push, no image ref — the
	// manifest template must never reference .Image.
	layout   string
	manifest string // repo-root-relative manifest template
	// rawManifest skips Guardian's Go-template renderer. This is for manifests
	// that intentionally contain another controller's template language, such
	// as Crossplane function-go-templating compositions.
	rawManifest bool
	// images is for components that render several image refs into one
	// manifest, such as cert-manager. Keys are the seed-registry repository
	// names and are exposed to templates as .Images.
	images []componentImage
	// enabled gates the component per site; nil converges on every site.
	// A manifest-only component MUST be gated (TestComponentsTable pins
	// this): with no image and no site gate there would be nothing
	// deliberate about where its objects land.
	enabled           func(*Site) bool
	publicHTTPService *publicHTTPService
}

type componentImage struct {
	name   string
	layout string
}

var components = []component{{
	name:     "openbao",
	layout:   "_main/src/infrastructure-components/openbao/image",
	manifest: "src/infrastructure-components/openbao/k8s/openbao.yaml.tmpl",
}, {
	name:     "crossplane",
	layout:   "_main/src/infrastructure-components/crossplane/image",
	manifest: "src/infrastructure-components/crossplane/k8s/crossplane.yaml.tmpl",
	enabled:  siteUsesEdgeGateway,
}, {
	name:     "cert-manager",
	manifest: "src/infrastructure-components/cert-manager/k8s/cert-manager.yaml.tmpl",
	enabled:  siteUsesPlatformTLS,
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
	name:     "provider-kubernetes",
	manifest: "src/infrastructure-components/crossplane-provider-kubernetes/k8s/provider-kubernetes.yaml",
	enabled:  siteUsesEdgeGateway,
}, {
	name:     "provider-kubernetes-config",
	manifest: "src/infrastructure-components/crossplane-provider-kubernetes/k8s/provider-kubernetes-config.yaml",
	enabled:  siteUsesEdgeGateway,
}, {
	name:        "edge-gateway-platform",
	manifest:    "src/platform/edge-gateway/k8s/edge-gateway-platform.yaml",
	rawManifest: true,
	enabled:     siteUsesEdgeGateway,
}, {
	name:     "aisucks",
	layout:   "_main/src/products/aisucks/services/api/image",
	manifest: "src/platform/public-http-service/k8s/public-http-service.yaml.tmpl",
	publicHTTPService: &publicHTTPService{
		namespace:       "aisucks",
		app:             "aisucks",
		domain:          func(s *Site) string { return s.Aisucks.Domain },
		podNetwork:      func(s *Site) bool { return s.Aisucks.PodNetwork },
		certDir:         "/var/lib/aisucks-certs",
		acmeEmail:       "im.shovonhasan@gmail.com",
		probeClusterIP:  "10.96.111.43",
		networkLabelKey: "platform.guardian.dev/network",
		memoryRequest:   "128Mi",
		memoryLimit:     "512Mi",
		goMemoryLimit:   "410MiB",
		cpuRequest:      "100m",
		healthPath:      "/healthz",
		readinessPeriod: 5,
	},
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
	// Scoped to the observability namespace, so it applies after the
	// victoria-metrics manifest creates that namespace.
	name:     "external-secrets",
	layout:   "_main/src/infrastructure-components/external-secrets/image",
	manifest: "src/infrastructure-components/external-secrets/k8s/external-secrets.yaml.tmpl",
}, {
	// Manifest-only: ESO store + projections for observability secrets.
	// After victoria-metrics because that manifest owns the observability
	// Namespace; before ClickHouse/Grafana because their pods require the
	// projected Secrets at container config time.
	name:     "guardian-secrets",
	manifest: "src/infrastructure-components/guardian-secrets/k8s/guardian-secrets.yaml.tmpl",
	enabled:  func(*Site) bool { return true },
}, {
	name:     "kube-state-metrics",
	layout:   "_main/src/infrastructure-components/kube-state-metrics/image",
	manifest: "src/infrastructure-components/kube-state-metrics/k8s/kube-state-metrics.yaml.tmpl",
}, {
	// clickhouse after victoria-metrics (the observability Namespace owner)
	// and before otel-collector: the collector's clickhouse exporter
	// retries, but there is no reason to start the logs pipeline with its
	// ledger behind. List order is apply order and no test pins this pair —
	// this comment carries the invariant. Site-gated like gateway (the
	// ledger ratchet: dev → gamma → prod); the otel-collector template
	// branches on the same flag, so a non-ledger site's collector renders
	// byte-identical to the metrics-only spine. The otel schema is NOT
	// applied here: docs/runbooks/ledger.md applies k8s/ddl/ by hand and
	// the exporter runs create_schema: false.
	name:     "clickhouse",
	layout:   "_main/src/infrastructure-components/clickhouse/image",
	manifest: "src/infrastructure-components/clickhouse/k8s/clickhouse.yaml.tmpl",
	enabled:  func(s *Site) bool { return s.Clickhouse.Enabled },
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
}, {
	// status after victoria-metrics: the page is rendered from queries
	// against the site-local VM. Sites without status.domains in site.yaml
	// render an empty manifest and deploy nothing (the apply loop skips
	// empty renders).
	name:     "status",
	layout:   "_main/src/status/image",
	manifest: "src/status/k8s/status.yaml.tmpl",
}, {
	// Crossplane-managed ESO projection for zot's publisher credentials.
	// The OpenBao values and auth role are reconciled earlier by guardian
	// up; this component only owns the Kubernetes resources that deliver
	// the credential to the zot pod.
	name:     "zot-secrets",
	manifest: "src/infrastructure-components/zot/k8s/zot-publisher-secrets.yaml.tmpl",
	enabled:  siteUsesPlatformTLS,
}, {
	name:     "zot",
	layout:   "_main/src/infrastructure-components/zot/image",
	manifest: "src/infrastructure-components/zot/k8s/zot.yaml.tmpl",
	enabled:  siteUsesPlatformTLS,
}}

func siteUsesEdgeGateway(s *Site) bool {
	return s.Gateway.Enabled
}

func siteUsesPlatformTLS(s *Site) bool {
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

type publicHTTPService struct {
	namespace       string
	app             string
	domain          func(*Site) string
	podNetwork      func(*Site) bool
	certDir         string
	acmeEmail       string
	probeClusterIP  string
	networkLabelKey string
	memoryRequest   string
	memoryLimit     string
	goMemoryLimit   string
	cpuRequest      string
	healthPath      string
	readinessPeriod int
}

type publicHTTPServiceRender struct {
	Namespace       string
	App             string
	Domain          string
	PodNetwork      bool
	Replicas        int
	ListenHTTP      string
	ListenTLS       string
	DiagAddr        string
	HTTPPort        int
	HTTPSPort       int
	MetricsPort     int
	CertDir         string
	ACMEEmail       string
	ProbeClusterIP  string
	NetworkLabelKey string
	NetworkLabelVal string
	MemoryRequest   string
	MemoryLimit     string
	GoMemoryLimit   string
	CPURequest      string
	HealthPath      string
	ReadinessPeriod int
	Gateway         struct {
		Enabled            bool
		Name               string
		Namespace          string
		TLSSectionName     string
		HTTPSectionName    string
		TLSRouteAPIVersion string
	}
}

func (c component) publicHTTPServiceRender(site *Site) *publicHTTPServiceRender {
	if c.publicHTTPService == nil {
		return nil
	}
	svc := c.publicHTTPService
	out := &publicHTTPServiceRender{
		Namespace:       svc.namespace,
		App:             svc.app,
		Domain:          svc.domain(site),
		PodNetwork:      svc.podNetwork(site),
		Replicas:        1,
		ListenHTTP:      ":80",
		ListenTLS:       ":443",
		DiagAddr:        "127.0.0.1:9090",
		HTTPPort:        80,
		HTTPSPort:       443,
		MetricsPort:     9090,
		CertDir:         svc.certDir,
		ACMEEmail:       svc.acmeEmail,
		ProbeClusterIP:  svc.probeClusterIP,
		NetworkLabelKey: svc.networkLabelKey,
		NetworkLabelVal: "pod",
		MemoryRequest:   svc.memoryRequest,
		MemoryLimit:     svc.memoryLimit,
		GoMemoryLimit:   svc.goMemoryLimit,
		CPURequest:      svc.cpuRequest,
		HealthPath:      svc.healthPath,
		ReadinessPeriod: svc.readinessPeriod,
	}
	out.Gateway.Enabled = site.Gateway.Enabled
	out.Gateway.Name = "edge"
	out.Gateway.Namespace = "gateway"
	out.Gateway.TLSSectionName = "tls-" + svc.app
	out.Gateway.HTTPSectionName = "http"
	out.Gateway.TLSRouteAPIVersion = "gateway.networking.k8s.io/v1alpha2"
	if out.PodNetwork {
		out.Replicas = 2
		out.ListenHTTP = ":8080"
		out.ListenTLS = ":8443"
		out.DiagAddr = ":9090"
		out.HTTPPort = 8080
		out.HTTPSPort = 8443
	}
	return out
}

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
