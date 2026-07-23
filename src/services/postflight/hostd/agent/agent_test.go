package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

const testRunnerClass = vm.Class("postflight-4-ubuntu-24.04-github-confidential")

type updateFloodDriver struct {
	*vm.Fake
	updates chan vm.ID
}

func (d *updateFloodDriver) Updates() <-chan vm.ID { return d.updates }

func TestRunPeriodicSyncCannotBePostponedByVMUpdates(t *testing.T) {
	var requests atomic.Int32
	first := make(chan struct{})
	second := make(chan struct{})
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == syncproto.JobPlanPath {
			if r.URL.Query().Get("cursor") != "" {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(syncproto.JobPlanSnapshot{Cursor: strings.Repeat("e", 64)})
			return
		}
		defer r.Body.Close()
		var report syncproto.SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Errorf("decode sync report: %v", err)
			http.Error(w, "invalid report", http.StatusBadRequest)
			return
		}
		switch requests.Add(1) {
		case 1:
			close(first)
		case 2:
			close(second)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(syncproto.SyncResponse{
			BootID: report.BootID, PoolTargets: map[string]int{string(testRunnerClass): 0},
		}); err != nil {
			t.Errorf("encode sync response: %v", err)
		}
	}))
	defer controlPlane.Close()

	driver := &updateFloodDriver{Fake: vm.NewFake(), updates: make(chan vm.ID)}
	agent, err := New(Config{
		HostID: "host-a", ControlPlaneOrigin: controlPlane.URL,
		Slots: map[vm.Class]int{testRunnerClass: 1}, Images: map[vm.Class]string{testRunnerClass: "golden"},
		SyncInterval: 20 * time.Millisecond, CheckoutGuestOrigin: "http://host.invalid",
		TraceDir: t.TempDir(), Platform: PlatformFingerprint{
			QEMUVersion: "QEMU 11.0.2", KernelRelease: "6.8.0", OSImageID: "ubuntu-24.04",
			MachineType: "pc-q35-11.0", CPUModel: "EPYC-v4", CRIUVersion: "Version: 4.2",
		},
	}, zvol.NewFake(), driver, "credential", make([]byte, 32), Options{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("initial control-plane sync did not arrive")
	}

	floodDone := make(chan struct{})
	go func() {
		for {
			select {
			case driver.updates <- "noise":
			case <-floodDone:
				return
			}
		}
	}()
	select {
	case <-second:
	case <-time.After(500 * time.Millisecond):
		close(floodDone)
		cancel()
		t.Fatal("continuous VM updates postponed periodic control-plane sync")
	}
	close(floodDone)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("agent run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not stop")
	}
}

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
	vms.OnDetach = func(device string) {
		for index := 0; index < slots; index++ {
			id := zvol.AssignmentID(fmt.Sprintf("assignment-%d", index))
			if device == "/dev/zvol/fake/ws/"+string(id) {
				volumes.SetAttached(id, false)
			}
			if device == "/dev/zvol/fake/tool-state/ws/"+string(id) {
				volumes.SetToolAttached(id, false)
			}
			if device == "/dev/zvol/fake/process-state/ws/"+string(id) {
				volumes.SetProcessAttached(id, false)
			}
		}
	}
	a, err := New(Config{
		HostID: "host-a", ControlPlaneOrigin: "https://control.invalid",
		Slots: map[vm.Class]int{testRunnerClass: slots}, Images: map[vm.Class]string{testRunnerClass: "golden"},
		SyncInterval: time.Second, CheckoutGuestOrigin: "http://host.invalid",
		TraceDir: t.TempDir(), Platform: PlatformFingerprint{
			QEMUVersion: "QEMU 11.0.2", KernelRelease: "6.8.0", OSImageID: "ubuntu-24.04",
			MachineType: "pc-q35-11.0", CPUModel: "EPYC-v4", CRIUVersion: "Version: 4.2",
		},
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
	a.Tick(context.Background())
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
		Tool:      syncproto.WorkspaceSpec{SizeBytes: 1 << 30},
		Process:   syncproto.ProcessSpec{SizeBytes: 1 << 30},
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

func TestImageRolloutReplacesRegisteredIdleListener(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	oldID := vm.ID(members[0].VMID)
	a.cfg.Images[testRunnerClass] = "golden-v2"
	vms.Images[testRunnerClass] = "golden-v2"

	a.Tick(context.Background())
	statuses, err := vms.List(context.Background())
	if err != nil || len(statuses) != 1 {
		t.Fatalf("replacement pool = %+v, %v", statuses, err)
	}
	if statuses[0].ID == oldID || statuses[0].Image != "golden-v2" || statuses[0].Phase != vm.PhaseBooting || statuses[0].MemberID != "" {
		t.Fatalf("replacement = %+v, old VM = %s", statuses[0], oldID)
	}
	destroy, launch := -1, -1
	for index, entry := range vms.Journal {
		if entry == "destroy "+string(oldID) {
			destroy = index
		}
		if strings.HasPrefix(entry, "launch "+string(statuses[0].ID)+" ") {
			launch = index
		}
	}
	if destroy < 0 || launch <= destroy {
		t.Fatalf("replacement did not destroy then refill: %v", vms.Journal)
	}
}

func TestImageRolloutPreservesDurablyAssignedListener(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	oldID := vm.ID(members[0].VMID)
	spec := assignmentSpec(0, members[0])
	a.HandleSync(syncproto.SyncResponse{
		BootID: a.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec},
		PoolTargets: map[string]int{string(testRunnerClass): 1},
	})
	a.cfg.Images[testRunnerClass] = "golden-v2"
	vms.Images[testRunnerClass] = "golden-v2"

	a.Tick(context.Background())
	statuses, err := vms.List(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].ID != oldID || statuses[0].Image != "golden" {
		t.Fatalf("assigned listener was replaced during rollout: %+v, %v", statuses, err)
	}
}

func TestImageRolloutPreservesLocallyAssignedListenerBeforeSync(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	oldID := vm.ID(members[0].VMID)
	assignVM(t, vms, members[0], assignmentSpec(0, members[0]))
	a.cfg.Images[testRunnerClass] = "golden-v2"
	vms.Images[testRunnerClass] = "golden-v2"

	a.Tick(context.Background())
	statuses, err := vms.List(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].ID != oldID || statuses[0].Image != "golden" || statuses[0].Assignment.RequestID == "" {
		t.Fatalf("locally assigned listener was replaced during rollout: %+v, %v", statuses, err)
	}
}

func TestPrepositionedPlanRendezvousesWithoutControlPlaneSync(t *testing.T) {
	a, vms, volumes := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	spec := assignmentSpec(0, members[0])
	plan := syncproto.JobPlan{
		PlanID: spec.AssignmentID, ExecutionID: spec.ExecutionID, AttemptID: spec.AttemptID,
		CheckRunID: spec.CheckRunID, RunID: spec.Identity.RunID, RunAttempt: spec.Identity.RunAttempt,
		JobDisplayName: spec.Identity.WorkflowJob, OrgID: spec.OrgID,
		InstallationID: spec.InstallationID, RepositoryID: spec.RepositoryID,
		RepositoryFullName: spec.RepositoryFullName, RunnerClass: spec.RunnerClass,
		Workspace: spec.Workspace, Tool: spec.Tool, Process: spec.Process,
	}
	if err := a.replaceJobPlans(context.Background(), syncproto.JobPlanSnapshot{
		Cursor: strings.Repeat("a", 64), Plans: []syncproto.JobPlan{plan},
	}); err != nil {
		t.Fatal(err)
	}
	assignVM(t, vms, members[0], spec)
	a.HandleVMUpdate(context.Background(), vm.ID(members[0].VMID))

	snapshot := a.Snapshot()
	if len(snapshot) != 1 || snapshot[0].AssignmentID != spec.AssignmentID || snapshot[0].State != syncproto.AssignmentBinding {
		t.Fatalf("local rendezvous snapshot = %+v", snapshot)
	}
	if !volumes.HasWorkspace(zvol.AssignmentID(spec.AssignmentID)) || !volumes.HasTool(zvol.AssignmentID(spec.AssignmentID)) || !volumes.HasProcess(zvol.AssignmentID(spec.AssignmentID)) {
		t.Fatal("local rendezvous did not materialize the complete durable tuple")
	}
	if _, found := vms.RendezvousFor(vm.ID(members[0].VMID)); !found {
		t.Fatal("local assignment did not dispatch rendezvous")
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
	raw, err := os.ReadFile(filepath.Join(a.cfg.TraceDir, "runner-0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]traceEvent{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event traceEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events[event.Event] = event
	}
	if ready := events["pool_ready"]; ready.RunID != "" || ready.AssignmentID != "" || ready.MemberID != members[0].MemberID {
		t.Fatalf("pool trace carried customer identity: %+v", ready)
	}
	if update := events["assignment_update_received"]; update.AssignmentID != assignments[0].AssignmentID || update.CheckRunID != assignments[0].CheckRunID {
		t.Fatalf("assignment trace did not preserve exact binding: %+v", update)
	}
	if mounts := events["mounts_ready"]; mounts.Restore == nil || mounts.Restore.Outcome != string(guestproto.RestoreColdFallback) || !mounts.Restore.ProcessInvalidated {
		t.Fatalf("cold fallback trace = %+v", mounts)
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
	destroyFailures := 1
	vms.Fail = func(op string, _ vm.ID) error {
		if op == "destroy" && destroyFailures > 0 {
			destroyFailures--
			return fmt.Errorf("injected destroy failure")
		}
		return nil
	}
	a.Tick(context.Background())
	if snapshot := a.Snapshot()[0]; snapshot.State == syncproto.AssignmentFailedClosed {
		t.Fatalf("reported fail-closed before VM recycle completed: %+v", snapshot)
	}
	if a.Metrics().FailedClosedAssignments.Load() != 0 {
		t.Fatal("failed-closed metric recorded before VM recycle completed")
	}
	a.Tick(context.Background())
	snapshot := a.Snapshot()[0]
	if snapshot.State != syncproto.AssignmentFailedClosed || snapshot.Restore == nil || snapshot.Restore.FailureClass != "integrity" {
		t.Fatalf("unsafe snapshot = %+v", snapshot)
	}
	if a.Metrics().FailedClosedAssignments.Load() != 1 {
		t.Fatal("failed-closed metric not recorded")
	}
	raw, err := os.ReadFile(filepath.Join(a.cfg.TraceDir, "runner-0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if destroy := strings.Index(string(raw), `"event":"vm_destroy_completed"`); destroy < 0 || destroy > strings.Index(string(raw), `"event":"assignment_failed_closed"`) {
		t.Fatalf("fail-closed was not preceded by proven VM destruction:\n%s", raw)
	}
}

func TestCrashedMemberFailsAcquiredAssignmentClosed(t *testing.T) {
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
	if got := a.Snapshot()[0].State; got != syncproto.AssignmentFailedClosed {
		t.Fatalf("crashed assignment state = %s", got)
	}
}

func TestProviderWithdrawalAbortsOnlyAfterVMDestruction(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	spec := assignmentSpec(0, members[0])
	assignVM(t, vms, members[0], spec)
	a.HandleSync(syncproto.SyncResponse{
		BootID: a.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec},
		PoolTargets: map[string]int{string(testRunnerClass): 1},
	})
	a.Tick(context.Background())

	spec.State = syncproto.DesiredAssignmentAbort
	a.HandleSync(syncproto.SyncResponse{
		BootID: a.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec},
		PoolTargets: map[string]int{string(testRunnerClass): 1},
	})
	a.Tick(context.Background())
	if got := a.Snapshot()[0].State; got != syncproto.AssignmentCompleted {
		t.Fatalf("withdrawn assignment state = %s", got)
	}
	if a.Metrics().FailedClosedAssignments.Load() != 0 {
		t.Fatal("provider withdrawal was counted as an infrastructure failure")
	}
	raw, err := os.ReadFile(filepath.Join(a.cfg.TraceDir, "runner-0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	destroyed := strings.Index(string(raw), `"event":"vm_destroy_completed"`)
	aborted := strings.Index(string(raw), `"event":"assignment_aborted"`)
	if destroyed < 0 || aborted <= destroyed {
		t.Fatalf("abort was not preceded by proven VM destruction:\n%s", raw)
	}
}

func TestTraceAdoptsCollectorSequenceAcrossHostdRestart(t *testing.T) {
	a, vms, volumes := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	a.closeTraceFiles()
	restarted, err := New(a.cfg, volumes, vms, "credential", make([]byte, 32), Options{
		NewID: func() string { return "restarted-hostd" },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.HandleSync(syncproto.SyncResponse{
		BootID: restarted.bootID, Members: members,
		PoolTargets: map[string]int{string(testRunnerClass): 1},
	})
	restarted.Tick(context.Background())
	spec := assignmentSpec(0, members[0])
	assignVM(t, vms, members[0], spec)
	restarted.HandleSync(syncproto.SyncResponse{
		BootID: restarted.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec},
		PoolTargets: map[string]int{string(testRunnerClass): 1},
	})
	restarted.Tick(context.Background())
	restarted.closeTraceFiles()
	raw, err := os.ReadFile(filepath.Join(a.cfg.TraceDir, "runner-0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	poolReady, assignmentUpdate := 0, 0
	var previous uint64
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event traceEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Seq <= previous {
			t.Fatalf("collector sequence moved from %d to %d", previous, event.Seq)
		}
		previous = event.Seq
		switch event.Event {
		case "pool_ready":
			poolReady++
		case "assignment_update_received":
			assignmentUpdate++
		}
	}
	if poolReady != 1 || assignmentUpdate != 1 {
		t.Fatalf("restart duplicated trace events: pool_ready=%d assignment_update=%d", poolReady, assignmentUpdate)
	}
}

func TestExitedRunnerCheckpointsBeforeVMTeardownAndSeals(t *testing.T) {
	a, vms, volumes := newTestAgent(t, 1)
	members := poolMembers(t, a, vms, 1)
	spec := assignmentSpec(0, members[0])
	assignVM(t, vms, members[0], spec)
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec}, PoolTargets: map[string]int{string(testRunnerClass): 1}})
	a.Tick(context.Background())
	if !vms.MarkBoundWithRestore(vm.ID(members[0].VMID), guestproto.RestoreStatus{Outcome: guestproto.RestoreNotRequested}) {
		t.Fatal("bind cold capsule")
	}
	a.Tick(context.Background())
	identity := vm.JobIdentity{
		RunID: spec.Identity.RunID, RunAttempt: spec.Identity.RunAttempt,
		RunnerName: spec.Identity.RunnerName, Repository: spec.Identity.Repository,
		WorkflowJob: spec.Identity.WorkflowJob,
	}
	clock := vm.ClockSample{UnixNS: time.Now().UnixNano(), Synchronized: true, Clocksource: "kvm-clock"}
	if !vms.MarkWorkerReady(vm.ID(members[0].VMID), clock) ||
		!vms.MarkHookBlocked(vm.ID(members[0].VMID), identity) ||
		!vms.MarkReady(vm.ID(members[0].VMID), clock) {
		t.Fatal("release customer steps")
	}
	a.Tick(context.Background())
	if !vms.MarkExited(vm.ID(members[0].VMID), 0) {
		t.Fatal("exit runner")
	}
	a.Tick(context.Background())
	report, err := a.Report(context.Background())
	if err != nil || len(report.Assignments) != 1 {
		t.Fatalf("assignment report = %+v, %v", report.Assignments, err)
	}
	assignmentReport := report.Assignments[0]
	if assignmentReport.State != syncproto.AssignmentExited || assignmentReport.Checkpoint == nil {
		t.Fatalf("checkpoint report = %+v", assignmentReport)
	}
	quiesce, destroy := -1, -1
	for index, entry := range vms.Journal {
		if strings.HasPrefix(entry, "quiesce ") {
			quiesce = index
		}
		if strings.HasPrefix(entry, "destroy ") {
			destroy = index
		}
	}
	if quiesce < 0 || destroy <= quiesce {
		t.Fatalf("VM teardown did not follow checkpoint: %v", vms.Journal)
	}
	spec.State = syncproto.DesiredAssignmentSeal
	spec.SealGeneration = "generation-sealed"
	spec.SealCheckpoint = assignmentReport.Checkpoint
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, Members: members, Assignments: []syncproto.DesiredAssignment{spec}, PoolTargets: map[string]int{string(testRunnerClass): 1}})
	a.Tick(context.Background())
	if got := a.Snapshot()[0]; got.State != syncproto.AssignmentSealed || got.SealedGeneration != spec.SealGeneration {
		t.Fatalf("sealed assignment = %+v", got)
	}
	if !volumes.HasGeneration(zvol.GenerationID(spec.SealGeneration)) {
		t.Fatal("atomic generation seal was not created")
	}
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, PoolTargets: map[string]int{string(testRunnerClass): 0}})
	a.Tick(context.Background())
	if len(a.traces) != 0 {
		t.Fatalf("retired trace files remain open: %d assignments=%+v zvol=%v", len(a.traces), a.Snapshot(), volumes.Journal)
	}
}
