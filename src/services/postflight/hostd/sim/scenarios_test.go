package sim

import (
	"context"
	"fmt"
	"testing"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

const runnerClass = vm.Class("postflight-4-ubuntu-24.04-github-confidential")

func listeningMembers(t *testing.T, world *World, count int) []syncproto.DesiredPoolMember {
	t.Helper()
	world.Sync(syncproto.SyncResponse{PoolTargets: map[string]int{string(runnerClass): count}})
	world.Tick()
	statuses, err := world.VMs.List(context.Background())
	if err != nil || len(statuses) != count {
		t.Fatalf("pool status=%v err=%v", statuses, err)
	}
	for _, status := range statuses {
		if !world.VMs.AdvanceBoot(status.ID) {
			t.Fatalf("boot %s", status.ID)
		}
	}
	members := make([]syncproto.DesiredPoolMember, 0, count)
	for index, report := range world.Report().Members {
		members = append(members, syncproto.DesiredPoolMember{
			MemberID: report.MemberID, VMID: report.VMID, State: syncproto.DesiredMemberListen,
			RunnerName: fmt.Sprintf("runner-%d", index), RunnerClass: string(runnerClass), JITConfig: "jit",
		})
	}
	world.Sync(syncproto.SyncResponse{Members: members, PoolTargets: map[string]int{string(runnerClass): count}})
	world.Tick()
	for _, member := range members {
		if !world.VMs.MarkListening(vm.ID(member.VMID)) {
			t.Fatalf("listen %s", member.MemberID)
		}
	}
	return members
}

func desiredAssignment(index int, member syncproto.DesiredPoolMember) syncproto.DesiredAssignment {
	return syncproto.DesiredAssignment{
		AssignmentID: fmt.Sprintf("assignment-%d", index), MemberID: member.MemberID,
		RequestID: fmt.Sprintf("request-%d", index), JobID: fmt.Sprintf("job-%d", index), CheckRunID: int64(1000 + index),
		State: syncproto.DesiredAssignmentRun, ExecutionID: fmt.Sprintf("execution-%d", index), AttemptID: "1",
		OrgID: "acme", InstallationID: 1, RepositoryID: 2, RepositoryFullName: "acme/widget",
		RunnerClass: string(runnerClass), Identity: syncproto.JobIdentity{
			RunID: fmt.Sprintf("10%d", index), RunAttempt: 1, RunnerName: member.RunnerName,
			Repository: "acme/widget", WorkflowJob: fmt.Sprintf("job-key-%d", index),
		},
		Workspace: syncproto.WorkspaceSpec{SizeBytes: 1 << 30},
		Tool:      syncproto.WorkspaceSpec{SizeBytes: 1 << 30},
		Process:   syncproto.ProcessSpec{SizeBytes: 1 << 30},
	}
}

func observeAssignment(t *testing.T, world *World, member syncproto.DesiredPoolMember, desired syncproto.DesiredAssignment) {
	t.Helper()
	identity := vm.JobIdentity{
		RunID: desired.Identity.RunID, RunAttempt: desired.Identity.RunAttempt,
		RunnerName: desired.Identity.RunnerName, Repository: desired.Identity.Repository,
		WorkflowJob: desired.Identity.WorkflowJob,
	}
	if !world.VMs.MarkAssigned(vm.ID(member.VMID), vm.Assignment{
		RequestID: desired.RequestID, JobID: desired.JobID, CheckRunID: desired.CheckRunID, RunnerName: member.RunnerName,
		JobDisplayName: desired.Identity.WorkflowJob, Identity: identity,
	}) {
		t.Fatalf("assign %s", member.MemberID)
	}
}

func TestConcurrentRendezvousAndRecoverableColdFallback(t *testing.T) {
	world := NewWorld(t, map[vm.Class]int{runnerClass: 6})
	members := listeningMembers(t, world, 6)
	desired := make([]syncproto.DesiredAssignment, 6)
	for index, member := range members {
		desired[index] = desiredAssignment(index, member)
		observeAssignment(t, world, member, desired[index])
		if world.Zvols.HasWorkspace(zvol.AssignmentID(desired[index].AssignmentID)) {
			t.Fatal("tenant workspace exists before control-plane assignment")
		}
	}
	world.Sync(syncproto.SyncResponse{Members: members, Assignments: desired, PoolTargets: map[string]int{string(runnerClass): 6}})
	world.Tick()
	for index, member := range members {
		restore := guestproto.RestoreStatus{Outcome: guestproto.RestoreSucceeded}
		if index == 0 {
			restore = guestproto.RestoreStatus{
				Outcome: guestproto.RestoreColdFallback, ProcessInvalidated: true,
				FailureClass: "incompatible", FailureCode: "criu-rejected",
			}
		}
		if !world.VMs.MarkBoundWithRestore(vm.ID(member.VMID), restore) {
			t.Fatalf("restore %s", member.MemberID)
		}
	}
	world.Tick()
	for index, member := range members {
		identity := vm.JobIdentity{
			RunID: desired[index].Identity.RunID, RunAttempt: 1,
			RunnerName: desired[index].Identity.RunnerName, Repository: "acme/widget",
			WorkflowJob: desired[index].Identity.WorkflowJob,
		}
		if !world.VMs.MarkWorkerReady(vm.ID(member.VMID), vm.ClockSample{Synchronized: true}) ||
			!world.VMs.MarkHookBlocked(vm.ID(member.VMID), identity) ||
			!world.VMs.MarkReady(vm.ID(member.VMID), vm.ClockSample{Synchronized: true}) {
			t.Fatalf("release %s", member.MemberID)
		}
	}
	world.Tick()
	if snapshot := world.Assignment("assignment-0"); snapshot.State != syncproto.AssignmentRunning ||
		snapshot.Restore == nil || snapshot.Restore.Outcome != string(guestproto.RestoreColdFallback) {
		t.Fatalf("cold fallback = %+v", snapshot)
	}
}

func TestMismatchedLocalAssignmentFailsClosedBeforeMaterialization(t *testing.T) {
	world := NewWorld(t, map[vm.Class]int{runnerClass: 1})
	members := listeningMembers(t, world, 1)
	desired := desiredAssignment(0, members[0])
	observeAssignment(t, world, members[0], desired)
	desired.RequestID = "different-request"
	world.Sync(syncproto.SyncResponse{Members: members, Assignments: []syncproto.DesiredAssignment{desired}, PoolTargets: map[string]int{string(runnerClass): 1}})
	world.Tick()
	if snapshot := world.Assignment(desired.AssignmentID); snapshot.State != syncproto.AssignmentFailedClosed {
		t.Fatalf("assignment = %+v", snapshot)
	}
	if world.Zvols.HasWorkspace(zvol.AssignmentID(desired.AssignmentID)) {
		t.Fatal("mismatched assignment materialized tenant state")
	}
}

func TestUnsafeRestoreRecyclesWithoutCustomerRelease(t *testing.T) {
	world := NewWorld(t, map[vm.Class]int{runnerClass: 1})
	members := listeningMembers(t, world, 1)
	desired := desiredAssignment(0, members[0])
	observeAssignment(t, world, members[0], desired)
	world.Sync(syncproto.SyncResponse{Members: members, Assignments: []syncproto.DesiredAssignment{desired}, PoolTargets: map[string]int{string(runnerClass): 1}})
	world.Tick()
	if !world.VMs.MarkRecycleRequired(vm.ID(members[0].VMID), guestproto.RestoreStatus{
		Outcome: guestproto.RestoreUnsafe, ProcessInvalidated: true,
		FailureClass: "integrity", FailureCode: "artifact-digest",
	}, "digest mismatch") {
		t.Fatal("mark unsafe restore")
	}
	world.Tick()
	if snapshot := world.Assignment(desired.AssignmentID); snapshot.State != syncproto.AssignmentFailedClosed || snapshot.VMID != "" {
		t.Fatalf("unsafe assignment = %+v", snapshot)
	}
}

func TestMemberCrashFailsAcquiredAssignmentClosedAndPoolRefills(t *testing.T) {
	world := NewWorld(t, map[vm.Class]int{runnerClass: 1})
	members := listeningMembers(t, world, 1)
	desired := desiredAssignment(0, members[0])
	observeAssignment(t, world, members[0], desired)
	world.Sync(syncproto.SyncResponse{Members: members, Assignments: []syncproto.DesiredAssignment{desired}, PoolTargets: map[string]int{string(runnerClass): 1}})
	world.Tick()
	if err := world.VMs.Destroy(context.Background(), vm.ID(members[0].VMID)); err != nil {
		t.Fatal(err)
	}
	world.Tick()
	if snapshot := world.Assignment(desired.AssignmentID); snapshot.State != syncproto.AssignmentFailedClosed {
		t.Fatalf("crashed assignment = %+v", snapshot)
	}
	statuses, err := world.VMs.List(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].Incarnation == members[0].MemberID {
		t.Fatalf("replacement pool=%+v err=%v", statuses, err)
	}
}
