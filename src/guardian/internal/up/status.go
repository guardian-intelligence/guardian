package up

import (
	"fmt"
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
	Code      string             `json:"code,omitempty" yaml:"code,omitempty" toml:"code,omitempty"`
	Summary   string             `json:"summary,omitempty" yaml:"summary,omitempty" toml:"summary,omitempty"`
	Detail    string             `json:"detail,omitempty" yaml:"detail,omitempty" toml:"detail,omitempty"`
	NextSteps []string           `json:"nextSteps,omitempty" yaml:"nextSteps,omitempty" toml:"nextSteps,omitempty"`
	Command   toolrunner.Command `json:"command,omitempty" yaml:"command,omitempty" toml:"command,omitempty"`
}

type StatusEvent struct {
	ID          string         `json:"id" yaml:"id" toml:"id"`
	ParentID    string         `json:"parentId,omitempty" yaml:"parentId,omitempty" toml:"parentId,omitempty"`
	ParentTitle string         `json:"parentTitle,omitempty" yaml:"parentTitle,omitempty" toml:"parentTitle,omitempty"`
	State       StatusState    `json:"state" yaml:"state" toml:"state"`
	Title       string         `json:"title" yaml:"title" toml:"title"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty" toml:"description,omitempty"`
	Detail      string         `json:"detail,omitempty" yaml:"detail,omitempty" toml:"detail,omitempty"`
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
		Description: "Verify destructive gates and genesis recipients",
	}
	renderStep = StepSpec{
		ID:          "render-manifests",
		ParentID:    "prepare",
		ParentTitle: "Prepare bootstrap",
		Title:       "Render manifests",
		Description: "Write Talos, Cozystack, and handoff manifests",
	}
)

var commandStepSpecs = map[string]StepSpec{
	"talm-init": {
		ID:          "talm-init",
		ParentID:    "talos",
		ParentTitle: "Install Talos",
		Title:       "Initialize Talm",
		Description: "Create the local Talos project state",
	},
	"talm-template": {
		ID:          "talm-template",
		ParentID:    "talos",
		ParentTitle: "Install Talos",
		Title:       "Render Talos config",
		Description: "Generate machine config for the target node",
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
		Title:       "Wait for Talos API",
		Description: "Wait for the Talos API to accept connections",
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
		Title:       "Install operator",
		Description: "Install the pinned Cozystack operator",
	},
	"kubectl-wait-cozystack-operator": {
		ID:          "kubectl-wait-cozystack-operator",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Wait for operator",
		Description: "Wait for the Cozystack operator rollout",
	},
	"kubectl-apply-platform": {
		ID:          "kubectl-apply-platform",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Apply platform package",
		Description: "Apply the default Cozystack package",
	},
	"kubectl-wait-platform-package": {
		ID:          "kubectl-wait-platform-package",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Wait for platform",
		Description: "Wait for the Cozystack package to become ready",
	},
	"kubectl-get-helmreleases": {
		ID:          "kubectl-get-helmreleases",
		ParentID:    "cozystack",
		ParentTitle: "Install Cozystack",
		Title:       "Read Helm releases",
		Description: "Capture Cozystack HelmRelease state",
	},
	"kubectl-apply-hello-world": {
		ID:          "kubectl-apply-hello-world",
		ParentID:    "handoff",
		ParentTitle: "Handoff",
		Title:       "Apply hello world",
		Description: "Apply the default handoff marker",
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
	if failure != nil {
		event.Detail = failure.Detail
	}
	reporter.Report(event)
}

func failureForCommand(cmd toolrunner.Command, spec StepSpec, err error) *StatusFailure {
	return failureFor(
		cmd.Name,
		fmt.Sprintf("%s failed", spec.Title),
		err.Error(),
		&cmd,
	)
}

func failureFor(code, summary, detail string, cmd *toolrunner.Command) *StatusFailure {
	failure := &StatusFailure{
		Code:      code,
		Summary:   summary,
		Detail:    detail,
		NextSteps: nextStepsForFailure(code, detail),
	}
	if cmd != nil {
		failure.Command = *cmd
	}
	return failure
}

func nextStepsForFailure(code, detail string) []string {
	switch {
	case code == "bootstrap.genesis.ageRecipients" || strings.Contains(detail, "ageRecipients"):
		return []string{
			"Add at least one public age recipient to bootstrap.genesis.ageRecipients.",
			"Keep the matching private identity outside the repo.",
		}
	case code == "bootstrap.safety":
		return []string{
			"Confirm the target host is safe to wipe.",
			"Set bootstrap.destructive and bootstrap.requireMaintenance only for an intended bootstrap run.",
		}
	case strings.Contains(detail, "LATITUDE_API_KEY"):
		return []string{
			"Export LATITUDE_API_KEY with a Latitude API token.",
			"Run the same guardian up command again.",
		}
	case strings.Contains(detail, "timed out") && strings.Contains(detail, "50000"):
		return []string{
			"Verify the host is powered on and reachable on the Talos API port.",
			"Check the provider console if the host did not reboot into Talos.",
		}
	default:
		return []string{
			"Review the full command detail with --status=plain.",
			"Run the same guardian up command again after correcting the failure.",
		}
	}
}
