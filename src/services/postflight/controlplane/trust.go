package main

import "strings"

// Trust classes for a workflow run, computed from the API-read run (never
// from the webhook payload). Stage (a) stores them on the demand; stage (b)
// uses them for capacity policy.
const (
	trustClassBranch  = "github_branch"
	trustClassPR      = "github_pr"
	trustClassPRFork  = "github_pr_fork"
	trustClassUnknown = "github_unknown"
)

// runObservation is the run-level evidence trust classification reads: the
// API run's event, head repository, head evidence, and resolved PR number.
type runObservation struct {
	Event                  string
	RepositoryFullName     string
	HeadRepositoryFullName string
	HeadBranch             string
	HeadSHA                string
	PullRequestNumber      int64 // 0 = no associated PR
}

// trustClassForRun grants branch trust only to an explicit event allowlist.
// Branch trust is a write capability (branch-trust runs seal workspace
// generations into their scope's lineage), and unenumerated events —
// workflow_run, issue_comment, merge_group, ... — are routinely triggered by
// fork PRs whose head branch is attacker-chosen, so they stay unknown.
func trustClassForRun(o runObservation) string {
	fork := o.HeadRepositoryFullName != "" && o.RepositoryFullName != "" &&
		!strings.EqualFold(o.HeadRepositoryFullName, o.RepositoryFullName)
	switch o.Event {
	case "pull_request", "pull_request_target":
		if fork {
			return trustClassPRFork
		}
		return trustClassPR
	case "push", "workflow_dispatch", "schedule":
		if fork {
			return trustClassUnknown
		}
		return trustClassBranch
	}
	if o.PullRequestNumber > 0 {
		if fork {
			return trustClassPRFork
		}
		return trustClassPR
	}
	return trustClassUnknown
}
