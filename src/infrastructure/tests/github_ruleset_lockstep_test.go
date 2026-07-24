package tests

import (
	"regexp"
	"strings"
	"testing"
)

// main's ruleset requires a status check by *name*. GitHub reports that name
// from the workflow job, so a rename on either side is a merge lock: the
// required context can never report, and every PR cut afterwards sits BLOCKED
// on a check that will never run — pending, not red, so nothing pages. That is
// exactly how #1132 locked the repository. These two literals must move
// together, and this test is the only thing that makes them.
func TestRulesetRequiredCheckMatchesTheGateWorkflow(t *testing.T) {
	rulesetPath := runfilePath("src/infrastructure/bootstrap/guardian-github/main.tf")
	ruleset := readText(t, rulesetPath)

	declared := regexp.MustCompile(`required_check\s*=\s*"([^"]+)"`).FindStringSubmatch(ruleset)
	if declared == nil {
		t.Fatalf("%s does not declare local.required_check", rulesetPath)
	}
	assertTextContains(t, ruleset, "context = local.required_check", rulesetPath)

	workflowPath := runfilePath(".github/workflows/build-and-test.yml")
	workflow := readText(t, workflowPath)

	jobs := jobKeys(t, workflow, workflowPath)
	if len(jobs) != 1 {
		t.Fatalf(
			"%s defines %d jobs %v; the ruleset requires exactly one context, so a second job would never be gated",
			workflowPath, len(jobs), jobs,
		)
	}
	if jobs[0] != declared[1] {
		t.Fatalf(
			"ruleset requires status check %q but %s reports %q; the mismatch merge-locks main",
			declared[1], workflowPath, jobs[0],
		)
	}

	// A `name:` on the job replaces the key as the reported context, so the
	// job key alone would stop being the thing GitHub checks against.
	if strings.Contains(jobsSection(t, workflow, workflowPath), "\n    name:") {
		t.Fatalf("%s overrides its job name; the reported check context is that name, not the job key", workflowPath)
	}
}

func jobsSection(t *testing.T, workflow, path string) string {
	t.Helper()
	start := strings.Index(workflow, "\njobs:\n")
	if start < 0 {
		t.Fatalf("%s has no jobs block", path)
	}
	return workflow[start:]
}

func jobKeys(t *testing.T, workflow, path string) []string {
	t.Helper()
	var keys []string
	for _, line := range strings.Split(jobsSection(t, workflow, path), "\n") {
		if match := regexp.MustCompile(`^  ([A-Za-z0-9_-]+):$`).FindStringSubmatch(line); match != nil {
			keys = append(keys, match[1])
		}
	}
	return keys
}

// The fleet the canary loop drives and the fleet OpenTofu manages are the same
// fleet. A repository added to one and not the other is either an unmanaged
// repository or a managed repository nothing exercises.
func TestSimulatedCustomerFleetIsFullyManaged(t *testing.T) {
	tofuPath := runfilePath("src/infrastructure/bootstrap/guardian-github/main.tf")
	managed := map[string]bool{}
	for _, match := range regexp.MustCompile(`\n    "([a-z0-9-]+)" = \{`).FindAllStringSubmatch(readText(t, tofuPath), -1) {
		managed[match[1]] = true
	}
	if len(managed) == 0 {
		t.Fatalf("%s declares no customer fleet repositories", tofuPath)
	}

	loopPath := runfilePath("src/infrastructure/deployments/postflight-runner/canary-loop.yaml")
	loop := readText(t, loopPath)
	exercised := map[string]bool{}
	for _, match := range regexp.MustCompile(`digital-guardian-software/([a-z0-9-]+)`).FindAllStringSubmatch(loop, -1) {
		exercised[match[1]] = true
	}
	for _, match := range regexp.MustCompile(`(?m)^\s+value: (postflight-canary|simulated-customer-[a-z]+)\s*$`).FindAllStringSubmatch(loop, -1) {
		exercised[match[1]] = true
	}
	if len(exercised) == 0 {
		t.Fatalf("%s references no simulated customer repositories", loopPath)
	}

	for repo := range exercised {
		if !managed[repo] {
			t.Fatalf("canary loop drives %q but OpenTofu does not manage it; add it to local.customer_fleet", repo)
		}
	}
	for repo := range managed {
		if !exercised[repo] {
			t.Fatalf("OpenTofu manages %q but no canary loop drives it; it bills nothing and proves nothing", repo)
		}
	}
}
