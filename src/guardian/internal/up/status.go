package up

import (
	"fmt"
	"time"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
)

type StatusState string

const (
	StatusRunning StatusState = "running"
	StatusDone    StatusState = "done"
	StatusSkipped StatusState = "skipped"
	StatusFailed  StatusState = "failed"
)

type StatusEvent struct {
	Name        string
	Index       int
	Total       int
	State       StatusState
	Title       string
	Description string
	Detail      string
	At          time.Time
}

type StatusReporter interface {
	Report(StatusEvent)
}

type NopStatusReporter struct{}

func (NopStatusReporter) Report(StatusEvent) {}

type StatusDescription struct {
	Title       string
	Description string
}

func DescribeStatus(name string, cfg config.Config) StatusDescription {
	switch name {
	case "talos-factory-schematic":
		return StatusDescription{
			Title:       "Register Talos image",
			Description: "pinning the Cozystack Talos schematic with the Talos Image Factory",
		}
	case "latitude-reinstall-ipxe":
		return StatusDescription{
			Title:       "Reimage host",
			Description: fmt.Sprintf("asking Latitude to boot %s from the pinned Talos iPXE URL", cfg.Node.Hostname),
		}
	case "wait-talos-maintenance":
		return StatusDescription{
			Title:       "Wait for maintenance",
			Description: "waiting for the Talos maintenance API and checking the install disk",
		}
	case "talm-init":
		return StatusDescription{
			Title:       "Initialize Talm",
			Description: "creating local Talos cluster secrets when no complete secret state exists",
		}
	case "write-talm-values":
		return StatusDescription{
			Title:       "Write Talm values",
			Description: "pinning cluster endpoint, CIDRs, installer image, and certificate SANs",
		}
	case "write-guardian-host-patch":
		return StatusDescription{
			Title:       "Write host patch",
			Description: "pinning hostname and install disk serial into the Talos machine patch",
		}
	case "talos-maintenance-disks":
		return StatusDescription{
			Title:       "Check install disk",
			Description: "verifying the configured disk serial is visible before destructive install",
		}
	case "talos-maintenance-links":
		return StatusDescription{
			Title:       "Check network link",
			Description: "verifying the configured NIC MAC is visible before destructive install",
		}
	case "talm-template":
		return StatusDescription{
			Title:       "Render machine config",
			Description: "rendering the Talos control-plane config from Talm and Guardian patches",
		}
	case "talm-dry-run":
		return StatusDescription{
			Title:       "Validate Talos apply",
			Description: "checking the Talos apply plan before mutating the host",
		}
	case "talm-apply":
		return StatusDescription{
			Title:       "Install Talos",
			Description: "applying Talos machine config and rebooting into the installed system",
		}
	case "wait-talos-api":
		return StatusDescription{
			Title:       "Wait for Talos API",
			Description: "waiting for the installed Talos node API to answer",
		}
	case "talm-bootstrap":
		return StatusDescription{
			Title:       "Bootstrap etcd",
			Description: "starting the Kubernetes control plane on the Talos node",
		}
	case "talm-kubeconfig":
		return StatusDescription{
			Title:       "Write kubeconfig",
			Description: "extracting the admin kubeconfig into local bootstrap state",
		}
	case "kubectl-wait-kubernetes-api":
		return StatusDescription{
			Title:       "Wait for Kubernetes",
			Description: "waiting for the Kubernetes API readiness endpoint",
		}
	case "write-genesis-bundle":
		return StatusDescription{
			Title:       "Write genesis bundle",
			Description: "age-encrypting local cluster bootstrap roots for offsite survival",
		}
	case "kubectl-remove-control-plane-taint":
		return StatusDescription{
			Title:       "Enable single-node scheduling",
			Description: "removing the control-plane taint when the cluster is configured for single-node bootstrap",
		}
	case "helm-install-cozystack":
		return StatusDescription{
			Title:       "Install Cozystack operator",
			Description: "installing the pinned Cozystack operator and package source",
		}
	case "kubectl-wait-cozystack-operator":
		return StatusDescription{
			Title:       "Wait for Cozystack operator",
			Description: "waiting for the Cozystack operator Deployment to roll out",
		}
	case "kubectl-apply-platform":
		return StatusDescription{
			Title:       "Apply platform package",
			Description: "declaring the Cozystack platform variant and platform values",
		}
	case "kubectl-wait-platform-package":
		return StatusDescription{
			Title:       "Wait for platform package",
			Description: "waiting for Cozystack to generate its Flux HelmRelease graph",
		}
	case "kubectl-get-helmreleases":
		return StatusDescription{
			Title:       "List Helm releases",
			Description: "showing the current Flux HelmRelease readiness surface",
		}
	case "kubectl-wait-node-ready":
		return StatusDescription{
			Title:       "Wait for node Ready",
			Description: "waiting for every Kubernetes node to report Ready",
		}
	case "kubectl-apply-hello-world":
		return StatusDescription{
			Title:       "Apply hello marker",
			Description: "creating the default Guardian handoff namespace and ConfigMap",
		}
	default:
		return StatusDescription{
			Title:       name,
			Description: "running bootstrap command",
		}
	}
}
