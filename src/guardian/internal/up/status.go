package up

import (
	"strings"
	"time"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/toolrunner"
)

type StatusState string

const (
	StatusPending StatusState = "pending"
	StatusRunning StatusState = "running"
	StatusDone    StatusState = "done"
	StatusSkipped StatusState = "skipped"
	StatusFailed  StatusState = "failed"
	StatusBlocked StatusState = "blocked"
)

type StatusFailure struct {
	Code    string             `json:"code,omitempty" yaml:"code,omitempty" toml:"code,omitempty"`
	Command toolrunner.Command `json:"command,omitempty" yaml:"command,omitempty" toml:"command,omitempty"`
}

type StatusEvent struct {
	ID          string         `json:"id" yaml:"id" toml:"id"`
	ParentID    string         `json:"parentId,omitempty" yaml:"parentId,omitempty" toml:"parentId,omitempty"`
	ParentTitle string         `json:"parentTitle,omitempty" yaml:"parentTitle,omitempty" toml:"parentTitle,omitempty"`
	State       StatusState    `json:"state" yaml:"state" toml:"state"`
	Title       string         `json:"title" yaml:"title" toml:"title"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty" toml:"description,omitempty"`
	Failure     *StatusFailure `json:"failure,omitempty" yaml:"failure,omitempty" toml:"failure,omitempty"`
	StartedAt   time.Time      `json:"startedAt,omitempty" yaml:"startedAt,omitempty" toml:"startedAt,omitempty"`
	EndedAt     time.Time      `json:"endedAt,omitempty" yaml:"endedAt,omitempty" toml:"endedAt,omitempty"`
}

type StatusReporter interface {
	Report(StatusEvent)
}

type StepSpec struct {
	ID          string
	ParentID    string
	ParentTitle string
	Title       string
	Description string
}

func commandStep(name string) StepSpec {
	if spec, ok := commandStepSpecs[name]; ok {
		return spec
	}
	return StepSpec{
		ID:          name,
		ParentID:    "bootstrap",
		ParentTitle: "Bootstrap host",
		Title:       titleFromID(name),
	}
}

func titleFromID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "Unknown step"
	}
	words := strings.Fields(strings.ReplaceAll(id, "-", " "))
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

var (
	openStateStep = StepSpec{
		ID:          "open-state",
		ParentID:    "prepare",
		ParentTitle: "Prepare bootstrap",
		Title:       "Open state",
		Description: "Use the local bootstrap state directory",
	}
	safetyStep = StepSpec{
		ID:          "check-bootstrap-safety",
		ParentID:    "prepare",
		ParentTitle: "Prepare bootstrap",
		Title:       "Check bootstrap safety",
		Description: "Verify stock Ubuntu target, destructive gates, and genesis recipients",
	}
)

var commandStepSpecs = map[string]StepSpec{
	"talm-init": {
		ID:          "talm-init",
		ParentID:    "ubuntu",
		ParentTitle: "Prepare Ubuntu host",
		Title:       "Initialize Talm",
		Description: "Create the local Talos project state",
	},
	"talm-template": {
		ID:          "talm-template",
		ParentID:    "ubuntu",
		ParentTitle: "Prepare Ubuntu host",
		Title:       "Render Talos config",
		Description: "Generate machine config offline from repo facts",
	},
	"write-talm-values": {
		ID:          "write-talm-values",
		ParentID:    "ubuntu",
		ParentTitle: "Prepare Ubuntu host",
		Title:       "Write Talm values",
		Description: "Pin cluster-wide values from repo facts",
	},
	"write-talm-template-overrides": {
		ID:          "write-talm-template-overrides",
		ParentID:    "ubuntu",
		ParentTitle: "Prepare Ubuntu host",
		Title:       "Pin Talm template facts",
		Description: "Use repo-owned host facts during Talm apply",
	},
	"boot-to-talos-install": {
		ID:          "boot-to-talos-install",
		ParentID:    "talos",
		ParentTitle: "Install Talos",
		Title:       "Install Talos from Ubuntu",
		Description: "Run the pinned boot-to-talos installer on the target disk",
	},
	"wait-talos-maintenance-api": {
		ID:          "wait-talos-maintenance-api",
		ParentID:    "talos",
		ParentTitle: "Install Talos",
		Title:       "Wait for Talos maintenance",
		Description: "Wait for the Talos API after boot-to-talos",
	},
	"talm-dry-run": {
		ID:          "talm-dry-run",
		ParentID:    "talos",
		ParentTitle: "Install Talos",
		Title:       "Validate Talos apply",
		Description: "Check the rendered Talos apply plan",
	},
	"talm-apply": {
		ID:          "talm-apply",
		ParentID:    "talos",
		ParentTitle: "Install Talos",
		Title:       "Apply Talos config",
		Description: "Apply machine config to the target node",
	},
	"wait-talos-api": {
		ID:          "wait-talos-api",
		ParentID:    "talos",
		ParentTitle: "Install Talos",
		Title:       "Wait for configured Talos",
		Description: "Wait for Talos API after applying machine config",
	},
	"talm-bootstrap": {
		ID:          "talm-bootstrap",
		ParentID:    "kubernetes",
		ParentTitle: "Bootstrap Kubernetes",
		Title:       "Bootstrap Kubernetes",
		Description: "Initialize the Kubernetes control plane",
	},
	"talm-kubeconfig": {
		ID:          "talm-kubeconfig",
		ParentID:    "kubernetes",
		ParentTitle: "Bootstrap Kubernetes",
		Title:       "Fetch kubeconfig",
		Description: "Write the local admin kubeconfig",
	},
	"kubectl-wait-kubernetes-api": {
		ID:          "kubectl-wait-kubernetes-api",
		ParentID:    "kubernetes",
		ParentTitle: "Bootstrap Kubernetes",
		Title:       "Wait for Kubernetes API",
		Description: "Wait for API server readiness",
	},
	"kubectl-wait-node-registered": {
		ID:          "kubectl-wait-node-registered",
		ParentID:    "kubernetes",
		ParentTitle: "Bootstrap Kubernetes",
		Title:       "Wait for node registration",
		Description: "Wait for the Talos node object",
	},
	"write-genesis-bundle": {
		ID:          "write-genesis-bundle",
		ParentID:    "kubernetes",
		ParentTitle: "Bootstrap Kubernetes",
		Title:       "Write genesis bundle",
		Description: "Archive bootstrap recovery material",
	},
	"kubectl-remove-control-plane-taint": {
		ID:          "kubectl-remove-control-plane-taint",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Allow local workloads",
		Description: "Remove the control-plane scheduling taint",
	},
	"helm-install-cozystack": {
		ID:          "helm-install-cozystack",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Run installer",
		Description: "Hand off to the pinned Cozystack installer",
	},
	"kubectl-wait-cozystack-operator": {
		ID:          "kubectl-wait-cozystack-operator",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Observe operator",
		Description: "Report Cozystack operator rollout status",
	},
	"write-cozystack-platform": {
		ID:          "write-cozystack-platform",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Render platform package",
		Description: "Write the Cozystack platform Package from repo facts",
	},
	"kubectl-apply-cozystack-platform": {
		ID:          "kubectl-apply-cozystack-platform",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Apply platform package",
		Description: "Create the Cozystack platform Package",
	},
	"kubectl-wait-platform-package": {
		ID:          "kubectl-wait-platform-package",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Observe platform package",
		Description: "Wait for the Cozystack Package controller",
	},
	"kubectl-wait-node-ready": {
		ID:          "kubectl-wait-node-ready",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Wait for node readiness",
		Description: "Wait for platform networking to make nodes Ready",
	},
	"kubectl-wait-cozystack-helmreleases": {
		ID:          "kubectl-wait-cozystack-helmreleases",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Observe platform releases",
		Description: "Wait for generated Cozystack HelmReleases",
	},
}

func reportStatus(reporter StatusReporter, spec StepSpec, state StatusState, now func() time.Time, failure *StatusFailure) {
	if reporter == nil {
		return
	}
	t := now()
	event := StatusEvent{
		ID:          spec.ID,
		ParentID:    spec.ParentID,
		ParentTitle: spec.ParentTitle,
		State:       state,
		Title:       spec.Title,
		Description: spec.Description,
		Failure:     failure,
	}
	switch state {
	case StatusRunning:
		event.StartedAt = t
	case StatusDone, StatusSkipped, StatusFailed, StatusBlocked:
		event.EndedAt = t
	}
	reporter.Report(event)
}

func failureForCommand(cmd toolrunner.Command) *StatusFailure {
	return failureFor(cmd.Name, &cmd)
}

func failureForCommandOutput(cmd toolrunner.Command) *StatusFailure {
	return failureFor(cmd.Name, &cmd)
}

func failureFor(code string, cmd *toolrunner.Command) *StatusFailure {
	failure := &StatusFailure{
		Code: code,
	}
	if cmd != nil {
		failure.Command = *cmd
	}
	return failure
}
