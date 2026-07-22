package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

const testRunnerClass = vm.Class("postflight-4-ubuntu-24.04-github-confidential")

func newTestAgent(t *testing.T, slots int) (*Agent, *vm.Fake, *zvol.Fake) {
	t.Helper()
	vms := vm.NewFake()
	vms.Images[testRunnerClass] = "golden"
	volumes := zvol.NewFake()
	vms.OnAttach = func(device string) {
		for index := 0; index < slots; index++ {
			id := zvol.AssignmentID(fmt.Sprintf("assignment-%d", index))
			if device == "/dev/zvol/fake/ws/"+string(id) {
				volumes.SetAttached(id, true)
			}
			if device == "/dev/zvol/fake/tool-state/ws/"+string(id) {
				volumes.SetToolAttached(id, true)
			}
			if device == "/dev/zvol/fake/process-state/ws/"+string(id) {
				volumes.SetProcessAttached(id, true)
			}
		}
	}
	a, err := New(Config{
		HostID: "host-a", ControlPlaneOrigin: "https://control.invalid",
		Slots: map[vm.Class]int{testRunnerClass: slots}, Images: map[vm.Class]string{testRunnerClass: "golden"},
		SyncInterval: time.Second, CheckoutGuestOrigin: "http://host.invalid",
	}, volumes, vms, "credential", make([]byte, 32), Options{NewID: func() string { return fmt.Sprintf("%02d", len(vms.Journal)) }})
	if err != nil {
		t.Fatal(err)
	}
	return a, vms, volumes
}

func poolMembers(t *testing.T, a *Agent, vms *vm.Fake, count int) []syncproto.DesiredPoolMember {
	t.Helper()
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, PoolTargets: map[string]int{string(testRunnerClass): count}})
	a.Tick(context.Background())
	statuses, err := vms.List(context.Background())
	if err != nil || len(statuses) != count {
		t.Fatalf("launched %d VMs: %v", len(statuses), err)
	}
	for _, status := range statuses {
		if !vms.AdvanceBoot(status.ID) {
			t.Fatalf("advance %s", status.ID)
		}
	}
	report, err := a.Report(context.Background())
	if err != nil || len(report.Members) != count {
		t.Fatalf("member report = %+v, %v", report.Members, err)
	}
	members := make([]syncproto.DesiredPoolMember, 0, count)
	for index, member := range report.Members {
		members = append(members, syncproto.DesiredPoolMember{
			MemberID: member.MemberID, VMID: member.VMID, State: syncproto.DesiredMemberListen,
			RunnerName: fmt.Sprintf("runner-%d", index), RunnerClass: string(testRunnerClass), JITConfig: "jit",
		})
	}
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, Members: members, PoolTargets: map[string]int{string(testRunnerClass): count}})
	a.Tick(context.Background())
	for _, member := range members {
		if !vms.MarkListening(vm.ID(member.VMID)) {
			t.Fatalf("member %s did not start listening", member.MemberID)
		}
	}
	return members
}

func assignmentSpec(index int, member syncproto.DesiredPoolMember) syncproto.DesiredAssignment {
	run := fmt.Sprintf("10%d", index)
	return syncproto.DesiredAssignment{
		AssignmentID: fmt.Sprintf("assignment-%d", index), MemberID: member.MemberID,
		RequestID: fmt.Sprintf("request-%d", index), JobID: fmt.Sprintf("job-%d", index), CheckRunID: int64(1000 + index),
		State: syncproto.DesiredAssignmentRun, ExecutionID: fmt.Sprintf("execution-%d", index), AttemptID: "1",
		OrgID: "acme", InstallationID: 1, RepositoryID: 2, RepositoryFullName: "acme/widget",
		RunnerClass: string(testRunnerClass),
		Identity: syncproto.JobIdentity{
			RunID: run, RunAttempt: 1, RunnerName: member.RunnerName,
			Repository: "acme/widget", WorkflowJob: fmt.Sprintf("job-key-%d", index),
		},
		Workspace: syncproto.WorkspaceSpec{SizeBytes: 1 << 30},
		Tool: syncproto.WorkspaceSpec{SizeBytes: 1 << 30},
		Process: syncproto.ProcessSpec{SizeBytes: 1 << 30},
	}
}

func assignVM(t *testing.T, vms *vm.Fake, member syncproto.DesiredPoolMember, spec syncproto.DesiredAssignment) {
	t.Helper()
	identity := vm.JobIdentity{
		RunID: spec.Identity.RunID, RunAttempt: spec.Identity.RunAttempt,
		RunnerName: spec.Identity.RunnerName, Repository: spec.Identity.Repository,
		WorkflowJob: spec.Identity.WorkflowJob,
	}
	if !vms.MarkAssigned(vm.ID(member.VMID), vm.Assignment{
		RequestID: spec.RequestID, JobID: spec.JobID, CheckRunID: spec.CheckRunID, RunnerName: member.RunnerName,
		JobDisplayName: spec.Identity.WorkflowJob, Identity: identity,
	}) {
		t.Fatalf("assign %s", member.MemberID)
	}
}

func TestSixListeningMembersBindConcurrentJobsExactly(t *testing.T) {
	a, vms, volumes := newTestAgent(t, 6)
	members := poolMembers(t, a, vms, 6)
	assignments := make([]syncproto.DesiredAssignment, 6)
	for index, member := range members {
		assignments[index] = assignmentSpec(index, member)
		assignVM(t, vms, member, assignments[index])
	}
	for index := range assignments {
		if volumes.HasWorkspace(zvol.AssignmentID(assignments[index].AssignmentID)) {
			t.Fatal("tenant volume existed before the immutable assignment response")
		}
	}
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, Members: members, Assignments: assignments, PoolTargets: map[string]int{string(testRunnerClass): 6}})
	a.Tick(context.Background())
	for index, spec := range assignments {
		if !volumes.HasWorkspace(zvol.AssignmentID(spec.AssignmentID)) || !volumes.HasTool(zvol.AssignmentID(spec.AssignmentID)) || !volumes.HasProcess(zvol.AssignmentID(spec.AssignmentID)) {
			t.Fatalf("assignment %s did not materialize its complete tuple", spec.AssignmentID)
		}
		restore := guestproto.RestoreStatus{Outcome: guestproto.RestoreSucceeded}
		if index == 0 {
			restore = guestproto.RestoreStatus{
				Outcome: guestproto.RestoreColdFallback, ProcessInvalidated: true,
				FailureClass: "incompatible", FailureCode: "criu-rejected",
			}
		}
		if !vms.MarkBoundWithRestore(vm.ID(members[index].VMID), restore) {
			t.Fatalf("bound %s", spec.AssignmentID)
		}
	}
	a.Tick(context.Background())
	for index, spec := range assignments {
		identity := vm.JobIdentity{
			RunID: spec.Identity.RunID, RunAttempt: 1, RunnerName: spec.Identity.RunnerName,
			Repository: spec.Identity.Repository, WorkflowJob: spec.Identity.WorkflowJob,
		}
		if !vms.MarkWorkerReady(vm.ID(members[index].VMID), vm.ClockSample{Synchronized: true}) ||
			!vms.MarkHookBlocked(vm.ID(members[index].VMID), identity) ||
			!vms.MarkReady(vm.ID(members[index].VMID), vm.ClockSample{Synchronized: true}) {
			t.Fatalf("release %s", spec.AssignmentID)
		}
	}
	a.Tick(context.Background())
	for _, snapshot := range a.Snapshot() {
		if snapshot.State != syncproto.AssignmentRunning {
			t.Fatalf("%s state = %s", snapshot.AssignmentID, snapshot.State)
		}
	}
	if a.Metrics().ColdFallbacks.Load() != 1 {
		t.Fatalf("cold fallbacks = %d", a.Metrics().ColdFallbacks.Load())
	}
}

func TestUnsafeRestoreFailsClosedWithoutReleasingWorker(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	spec := assignmentSpec(0, members[0])
	assignVM(t, vms, members[0], spec)
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec}, PoolTargets: map[string]int{string(testRunnerClass): 1}})
	a.Tick(context.Background())
	if !vms.MarkRecycleRequired(vm.ID(members[0].VMID), guestproto.RestoreStatus{
		Outcome: guestproto.RestoreUnsafe, ProcessInvalidated: true,
		FailureClass: "integrity", FailureCode: "artifact-digest",
	}, "digest mismatch") {
		t.Fatal("mark recycle")
	}
	a.Tick(context.Background())
	snapshot := a.Snapshot()[0]
	if snapshot.State != syncproto.AssignmentFailedClosed || snapshot.Restore == nil || snapshot.Restore.FailureClass != "integrity" {
		t.Fatalf("unsafe snapshot = %+v", snapshot)
	}
	if a.Metrics().FailedClosedAssignments.Load() != 1 {
		t.Fatal("failed-closed metric not recorded")
	}
}

func TestCrashedMemberRequeuesAssignment(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	spec := assignmentSpec(0, members[0])
	assignVM(t, vms, members[0], spec)
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec}, PoolTargets: map[string]int{string(testRunnerClass): 1}})
	a.Tick(context.Background())
	if err := vms.Destroy(context.Background(), vm.ID(members[0].VMID)); err != nil {
		t.Fatal(err)
	}
	a.Tick(context.Background())
	if got := a.Snapshot()[0].State; got != syncproto.AssignmentRequeued {
		t.Fatalf("crashed assignment state = %s", got)
	}
}
