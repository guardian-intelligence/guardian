package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/postflight/hostd/zvol"
)

const turboClass = "postflight-4vcpu-ubuntu24-turbo"

// Start with no Turbo demand, pools, generations, or listeners. The first
// API-confirmed job must bootstrap the pool; its successful branch build must
// seed a generation that the next VM uses without inheriting process state.
func TestTurboFirstDemandBootstrapsAndReusesDiskGeneration(t *testing.T) {
	control := startE2EControlPlane(t)
	ctx := context.Background()
	var cpu int
	var memory, workspace, tool int64
	var confidential string
	if err := control.pool.QueryRow(ctx, `SELECT cpu_cores, memory_bytes, disk_bytes,
        tool_disk_bytes, confidential_technology FROM runner_classes WHERE class = $1`, turboClass).
		Scan(&cpu, &memory, &workspace, &tool, &confidential); err != nil {
		t.Fatal(err)
	}
	if cpu != 4 || memory != 16<<30 || workspace != 80<<30 || tool != 32<<30 || confidential != "" {
		t.Fatalf("Turbo dimensions = %d/%d/%d/%d, confidential=%q", cpu, memory, workspace, tool, confidential)
	}
	if got := queryString(t, control.pool, `SELECT confidential_technology FROM runner_classes WHERE class = $1`, e2eClass); got != "sev-snp" {
		t.Fatalf("confidential class changed: %q", got)
	}
	_, vms, volumes, _ := startE2EHostForClass(t, control.server.URL, "rust-forge-test", turboClass, 2)
	waitFor(t, "host registered before first demand", func() bool {
		return queryString(t, control.pool, `SELECT count(*)::text FROM host_slots WHERE host_id = 'rust-forge-test'`) == "1"
	})
	if got := queryString(t, control.pool, `SELECT count(*)::text FROM runner_pools WHERE runner_class = $1`, turboClass); got != "0" {
		t.Fatalf("Turbo pool existed before first demand: %s", got)
	}

	var previousGeneration string
	for attempt := int64(1); attempt <= 2; attempt++ {
		runID, jobID, checkID := 810+attempt, 9100+attempt, 8100+attempt
		run := apiWorkflowRun{ID: runID, Event: "workflow_dispatch", Path: ".github/workflows/build-and-test.yml",
			HeadBranch: "main", HeadSHA: strings.Repeat("b", 40), RunAttempt: 1}
		run.Repository.ID, run.Repository.FullName, run.HeadRepository.FullName = e2eRepositoryID, e2eRepo, e2eRepo
		job := apiWorkflowJob{ID: jobID, RunID: runID, RunAttempt: 1, Name: "build", Status: "queued",
			Labels: []string{turboClass}, HeadSHA: run.HeadSHA, HeadBranch: "main",
			CheckRunURL: fmt.Sprintf("https://api.github.com/repos/%s/check-runs/%d", e2eRepo, checkID)}
		control.github.setRun(run, []apiWorkflowJob{job})
		event := jobEvent{Action: "queued", InstallationID: e2eInstallationID,
			RepositoryID: e2eRepositoryID, RepositoryFullName: e2eRepo,
			Job: workflowJobPayload{ID: jobID, RunID: runID, RunAttempt: 1}}
		if err := control.worker.submitQueuedJob(ctx, event, fmt.Sprintf("turbo-queued-%d", attempt)); err != nil {
			t.Fatal(err)
		}
		var selected vm.Status
		waitFor(t, "Turbo listener", func() bool {
			statuses, _ := vms.List(ctx)
			for _, status := range statuses {
				if status.Phase == vm.PhaseListening {
					selected = status
					return true
				}
			}
			return false
		})
		runner := queryString(t, control.pool, `SELECT runner_name FROM runner_pool_members WHERE member_id = $1`, selected.Incarnation)
		identity := vm.JobIdentity{RunID: strconv.FormatInt(runID, 10), RunAttempt: 1,
			RunnerName: runner, Repository: e2eRepo, WorkflowJob: "build"}
		if !vms.MarkAssigned(selected.ID, vm.Assignment{RequestID: fmt.Sprintf("turbo-request-%d", attempt),
			JobID: fmt.Sprintf("turbo-protocol-%d", attempt), CheckRunID: checkID,
			RunnerName: runner, JobDisplayName: "build", Identity: identity}) {
			t.Fatal("Turbo listener rejected assignment")
		}
		var rendezvous vm.Rendezvous
		waitFor(t, "Turbo disk binding", func() bool {
			var found bool
			rendezvous, found = vms.RendezvousFor(selected.ID)
			return found
		})
		if rendezvous.CheckpointDigest != "" || rendezvous.CheckpointVersion != "" {
			t.Fatalf("Turbo inherited process credentials: %+v", rendezvous)
		}
		assignmentID := zvol.AssignmentID(rendezvous.AssignmentID)
		if !volumes.HasWorkspace(assignmentID) || !volumes.HasTool(assignmentID) {
			t.Fatal("workspace and tool volumes were not both materialized")
		}
		_, workspaces, err := volumes.Inventory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, volume := range workspaces {
			if strings.HasSuffix(volume.Name, "/ws/"+rendezvous.AssignmentID) && string(volume.Source) != previousGeneration {
				t.Fatalf("workspace source = %q, want %q", volume.Source, previousGeneration)
			}
		}
		if !vms.MarkBound(selected.ID) {
			t.Fatal("Turbo volumes did not bind")
		}
		waitFor(t, "Turbo authorization", func() bool {
			_, found := vms.AuthorizationFor(selected.ID)
			return found
		})
		clock := vm.ClockSample{UnixNS: time.Now().UnixNano(), Synchronized: true, Clocksource: "kvm-clock"}
		if !vms.MarkWorkerReady(selected.ID, clock) || !vms.MarkHookBlocked(selected.ID, identity) || !vms.MarkReady(selected.ID, clock) {
			t.Fatal("Turbo worker did not start")
		}
		waitFor(t, "running Turbo assignment", func() bool {
			return queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE provider_job_id = $1`, jobID) == "running"
		})
		if !vms.MarkExited(selected.ID, 0) {
			t.Fatal("Turbo runner did not exit")
		}
		waitFor(t, "sealed disk candidate", func() bool {
			return queryString(t, control.pool, `SELECT state FROM runner_job_assignments WHERE provider_job_id = $1`, jobID) == "sealed"
		})
		generation := queryString(t, control.pool, `SELECT seal_generation FROM runner_job_assignments WHERE provider_job_id = $1`, jobID)
		if got := queryString(t, control.pool, `SELECT state FROM workspace_generations WHERE generation = $1`, generation); got != "candidate" {
			t.Fatalf("generation promoted before GitHub success: %q", got)
		}
		job.Status, job.Conclusion = "completed", "success"
		control.github.setRun(run, []apiWorkflowJob{job})
		event.Action = "completed"
		if err := control.worker.refreshRunAndJobs(ctx, event, fmt.Sprintf("turbo-completed-%d", attempt)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "GitHub-confirmed generation promotion", func() bool {
			return queryString(t, control.pool, `SELECT state FROM workspace_generations WHERE generation = $1`, generation) == "committed"
		})
		if got := queryString(t, control.pool, `SELECT source_generation FROM runner_job_assignments WHERE provider_job_id = $1`, jobID); got != previousGeneration {
			t.Fatalf("assignment cache lineage = %q, want %q", got, previousGeneration)
		}
		previousGeneration = generation
	}
	status, body := requestHostStatus(t, control.server.URL, "rust-forge-test", e2eSyncSecret)
	if status != http.StatusOK || !strings.Contains(body, previousGeneration) || !strings.Contains(body, `"source_generation"`) {
		t.Fatalf("operator status omitted generation evidence: %d %s", status, body)
	}
	for _, forbidden := range []string{e2eJITBlob, "jit_config", e2eSyncSecret, "private_key", "payload_json"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("operator status exposed %q", forbidden)
		}
	}
}

func requestHostStatus(t *testing.T, origin, hostID, token string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, origin+hostdStatusPath+"?host_id="+hostID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode == http.StatusOK && (!json.Valid(body) || response.Header.Get("Cache-Control") != "no-store") {
		t.Fatal("status must be uncacheable JSON")
	}
	return response.StatusCode, string(body)
}

func TestHostStatusRequiresHostCredential(t *testing.T) {
	control := startE2EControlPlane(t)
	for _, token := range []string{"", "wrong-secret"} {
		if status, _ := requestHostStatus(t, control.server.URL, "missing", token); status != http.StatusUnauthorized {
			t.Fatalf("unauthorized status = %d", status)
		}
	}
	if status, _ := requestHostStatus(t, control.server.URL, "missing", e2eSyncSecret); status != http.StatusNotFound {
		t.Fatalf("unknown host status = %d", status)
	}
	if status, _ := requestHostStatus(t, control.server.URL, "", e2eSyncSecret); status != http.StatusBadRequest {
		t.Fatalf("missing host_id status = %d", status)
	}
}
