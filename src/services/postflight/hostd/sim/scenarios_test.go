package sim

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

var errAssignTimeout = errors.New("qmp timeout delivering assignment")

const class = "postflight-4cpu-ubuntu-2404"

func slots(n int) map[vm.Class]int { return map[vm.Class]int{class: n} }

func runLease(id string) syncproto.DesiredLease {
	return syncproto.DesiredLease{
		LeaseID:              id,
		State:                syncproto.DesiredRun,
		ExecutionID:          "exec-" + id,
		AttemptID:            "attempt-" + id,
		OrgID:                "guardian-intelligence",
		InstallationID:       42,
		RepositoryID:         4242,
		RepositoryFullName:   "guardian-intelligence/postflight-tracer",
		RunnerClass:          class,
		JITConfig:            "jit-" + id,
		ProviderRunID:        101,
		ProviderJobID:        201,
		ProviderRunAttempt:   1,
		JobDisplayName:       "test",
		AssignedRunnerName:   id,
		RendezvousAuthorized: true,
		Workspace:            syncproto.WorkspaceSpec{SizeBytes: 1 << 30},
	}
}

func deliver(world *World, targets int, leases ...syncproto.DesiredLease) {
	world.Sync(syncproto.SyncResponse{
		Leases:      leases,
		PoolTargets: map[string]int{class: targets},
	})
}

// driveToReady walks one lease through pending → ready, asserting each hop.
func driveToReady(t *testing.T, world *World, spec syncproto.DesiredLease) (vmID string) {
	t.Helper()
	deliver(world, 1, spec)
	world.Tick() // materialize; pool launches a warm VM
	if got := world.Lease(spec.LeaseID).State; got != syncproto.StateClaiming {
		t.Fatalf("after tick 1: state %s, want claiming", got)
	}
	bootAll(world)
	world.Tick() // claim the warm VM and prepare the listener
	snapshot := world.Lease(spec.LeaseID)
	if snapshot.State != syncproto.StateAssigning || snapshot.VMID == "" {
		t.Fatalf("after tick 2: state %s vm %q, want assigning with a vm", snapshot.State, snapshot.VMID)
	}
	completeRendezvous(t, world, spec, snapshot.VMID)
	if got := world.Lease(spec.LeaseID).State; got != syncproto.StateReady {
		t.Fatalf("after rendezvous: state %s, want ready", got)
	}
	return snapshot.VMID
}

func completeRendezvous(t *testing.T, world *World, spec syncproto.DesiredLease, vmID string) {
	t.Helper()
	world.VMs.MarkListening(vm.ID(vmID))
	world.Tick()
	if got := world.Lease(spec.LeaseID).State; got != syncproto.StateListening {
		t.Fatalf("after registration: state %s, want listening", got)
	}
	identity := vm.JobIdentity{
		RunID: "101", RunAttempt: 1, RunnerName: spec.LeaseID,
		Repository: spec.RepositoryFullName, WorkflowJob: "test",
	}
	world.VMs.MarkAssigned(vm.ID(vmID), vm.Assignment{
		RequestID: "request-" + spec.LeaseID, JobID: "runner-job-" + spec.LeaseID,
		RunnerName: spec.LeaseID, JobDisplayName: spec.JobDisplayName, Identity: identity,
	})
	world.Tick()
	if got := world.Lease(spec.LeaseID).State; got != syncproto.StateBinding {
		t.Fatalf("after local assignment: state %s, want binding", got)
	}
	world.VMs.MarkBound(vm.ID(vmID))
	world.Tick()
	if got := world.Lease(spec.LeaseID).State; got != syncproto.StateAuthorizing {
		t.Fatalf("after restore: state %s, want authorizing", got)
	}
	world.VMs.MarkWorkerReady(vm.ID(vmID), vm.ClockSample{
		UnixNS: time.Now().UnixNano(), Synchronized: true, Clocksource: "kvm-clock",
	})
	world.Tick()
	world.VMs.MarkHookBlocked(vm.ID(vmID), identity)
	world.Tick()
	world.VMs.MarkReady(vm.ID(vmID), vm.ClockSample{
		UnixNS: time.Now().UnixNano(), Synchronized: true, Clocksource: "kvm-clock",
	})
	world.Tick()
}

func driveToHookBlocked(t *testing.T, world *World, spec syncproto.DesiredLease) string {
	t.Helper()
	deliver(world, 1, spec)
	world.Tick()
	bootAll(world)
	world.Tick()
	snapshot := world.Lease(spec.LeaseID)
	world.VMs.MarkListening(vm.ID(snapshot.VMID))
	world.Tick()
	identity := vm.JobIdentity{
		RunID: "101", RunAttempt: 1, RunnerName: spec.LeaseID,
		Repository: spec.RepositoryFullName, WorkflowJob: "test",
	}
	world.VMs.MarkAssigned(vm.ID(snapshot.VMID), vm.Assignment{
		RequestID: "request-" + spec.LeaseID, JobID: "runner-job-" + spec.LeaseID,
		RunnerName: spec.LeaseID, JobDisplayName: spec.JobDisplayName, Identity: identity,
	})
	world.Tick()
	world.VMs.MarkBound(vm.ID(snapshot.VMID))
	world.Tick()
	world.VMs.MarkWorkerReady(vm.ID(snapshot.VMID), vm.ClockSample{})
	world.Tick()
	world.VMs.MarkHookBlocked(vm.ID(snapshot.VMID), identity)
	world.Tick()
	return snapshot.VMID
}

func driveExistingToReady(t *testing.T, world *World, spec syncproto.DesiredLease, vmID string) {
	t.Helper()
	status, err := world.VMs.Status(context.Background(), vm.ID(vmID))
	if err != nil {
		t.Fatal(err)
	}
	identity := vm.JobIdentity{
		RunID: "101", RunAttempt: 1, RunnerName: spec.LeaseID,
		Repository: spec.RepositoryFullName, WorkflowJob: "test",
	}
	switch status.Phase {
	case vm.PhaseAssigned:
		world.VMs.MarkListening(vm.ID(vmID))
		world.Tick()
		fallthrough
	case vm.PhaseListening:
		world.VMs.MarkAssigned(vm.ID(vmID), vm.Assignment{
			RequestID: "request-" + spec.LeaseID, JobID: "runner-job-" + spec.LeaseID,
			RunnerName: spec.LeaseID, JobDisplayName: spec.JobDisplayName, Identity: identity,
		})
		world.Tick()
		fallthrough
	case vm.PhaseJobAssigned:
		world.VMs.MarkBound(vm.ID(vmID))
		world.Tick()
		fallthrough
	case vm.PhaseBound:
		world.VMs.MarkWorkerReady(vm.ID(vmID), vm.ClockSample{})
		world.Tick()
		fallthrough
	case vm.PhaseWorkerReady:
		world.VMs.MarkHookBlocked(vm.ID(vmID), identity)
		world.Tick()
		fallthrough
	case vm.PhaseHookBlocked:
		world.VMs.MarkReady(vm.ID(vmID), vm.ClockSample{
			UnixNS: time.Now().UnixNano(), Synchronized: true, Clocksource: "kvm-clock",
		})
		world.Tick()
	case vm.PhaseReady:
	}
}

// bootAll flips every booting VM to warm.
func bootAll(world *World) {
	statuses, _ := world.VMs.List(context.Background())
	for _, status := range statuses {
		if status.Phase == vm.PhaseBooting {
			world.VMs.AdvanceBoot(status.ID)
		}
	}
}

func TestHappyPathRunSealForget(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	vmID := driveToReady(t, world, spec)

	// The assignment the guest received carries the workspace device, the
	// JIT blob, and a checkout token derived from this host's secret.
	preparation, ok := world.VMs.Preparation(vm.ID(vmID))
	if !ok {
		t.Fatal("assigned vm holds no preparation")
	}
	rendezvous, ok := world.VMs.RendezvousFor(vm.ID(vmID))
	if !ok {
		t.Fatal("assigned vm holds no rendezvous")
	}
	if rendezvous.WorkspaceDevice != "/dev/zvol/fake/ws/l1" {
		t.Fatalf("workspace device %q", rendezvous.WorkspaceDevice)
	}
	if preparation.JITConfig != "jit-l1" {
		t.Fatalf("jit config %q", preparation.JITConfig)
	}
	wantToken := checkoutbundle.DeriveCheckoutToken([]byte("0123456789abcdef0123456789abcdef"), "exec-l1", "attempt-l1")
	authorization, ok := world.VMs.AuthorizationFor(vm.ID(vmID))
	if !ok || authorization.Env["POSTFLIGHT_CHECKOUT_TOKEN"] != wantToken {
		t.Fatal("checkout token does not match host-secret derivation")
	}
	// While the lease is live, its token resolves.
	if _, ok, _ := world.Agent.ResolveActiveLease(context.Background(), "exec-l1", "attempt-l1"); !ok {
		t.Fatal("live lease does not resolve for checkout")
	}

	world.VMs.MarkExited(vm.ID(vmID), 0)
	world.Tick()
	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateExited || snapshot.ExitCode != 0 {
		t.Fatalf("after exit: %+v", snapshot)
	}
	// Destroy-and-refill freed the slot: the exited VM is gone.
	if status, _ := world.VMs.Status(context.Background(), vm.ID(vmID)); status.Phase != vm.PhaseGone {
		t.Fatalf("exited vm still present: %v", status.Phase)
	}

	// Control plane decides: seal as generation g1.
	sealed := spec
	sealed.State = syncproto.DesiredSeal
	sealed.SealGeneration = "g1"
	sealed.SealCheckpoint = &syncproto.CheckpointArtifact{
		Digest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Version: "Version: 4.2",
	}
	deliver(world, 1, sealed)
	world.Tick()
	if got := world.Lease("l1"); got.State != syncproto.StateSealed || got.SealedGeneration != "g1" {
		t.Fatalf("after seal: %+v", got)
	}
	if !world.Zvols.HasGeneration("g1") {
		t.Fatal("sealed generation not resident")
	}

	// Omission is the ack: workspace destroyed, lease forgotten, generation kept.
	deliver(world, 1)
	world.Tick()
	if world.HasLease("l1") {
		t.Fatalf("acknowledged lease still tracked: snapshot=%+v process=%t attached=%t journal=%v", world.Lease("l1"), world.Zvols.HasProcess("l1"), world.Zvols.ProcessAttached("l1"), world.Zvols.Journal)
	}
	if world.Zvols.HasWorkspace("l1") {
		t.Fatal("workspace survived collection")
	}
	if !world.Zvols.HasGeneration("g1") {
		t.Fatal("generation destroyed without a reap verb")
	}
}

func TestSixListenerPoolRoutesCrossedAssignmentsLocally(t *testing.T) {
	world := NewWorld(t, slots(6))
	repos := []string{
		"guardian-intelligence/turborepo-tuned",
		"guardian-intelligence/cilium-tuned",
		"guardian-intelligence/envoy-tuned",
		"guardian-intelligence/calcom-tuned",
		"guardian-intelligence/gradle-tuned",
		"guardian-intelligence/llvm-tuned",
	}
	specs := make([]syncproto.DesiredLease, 6)
	for i := range specs {
		specs[i] = runLease("listener-" + string(rune('a'+i)))
		specs[i].ExecutionLeaseID = specs[i].LeaseID
		specs[i].ExecutionID = "execution-" + specs[i].LeaseID
		specs[i].AttemptID = "attempt-" + specs[i].LeaseID
		specs[i].RepositoryFullName = repos[i]
		specs[i].ProviderRunID = int64(1001 + i)
		specs[i].ProviderJobID = int64(2001 + i)
		specs[i].JobDisplayName = "benchmark"
	}
	deliver(world, 6, specs...)
	world.Tick()
	bootAll(world)
	world.Tick()

	vmByListener := map[string]vm.ID{}
	statuses, err := world.VMs.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.Lease != "" {
			vmByListener[status.Lease] = status.ID
			if !world.VMs.MarkListening(status.ID) {
				t.Fatalf("listener %s did not register", status.Lease)
			}
		}
	}
	world.Tick()

	// Rotate the jobs one position. Every listener receives a different
	// execution than the one whose demand caused its registration.
	for i, listener := range specs {
		target := specs[(i+1)%len(specs)]
		id := vmByListener[listener.LeaseID]
		identity := vm.JobIdentity{
			RunID: strconv.FormatInt(target.ProviderRunID, 10), RunAttempt: target.ProviderRunAttempt,
			RunnerName: listener.LeaseID, Repository: target.RepositoryFullName, WorkflowJob: "benchmark",
		}
		if !world.VMs.MarkAssigned(id, vm.Assignment{
			RequestID: "request-" + target.LeaseID, JobID: "runner-job-" + target.LeaseID,
			RunnerName: listener.LeaseID, JobDisplayName: target.JobDisplayName, Identity: identity,
		}) {
			t.Fatalf("listener %s did not observe assignment", listener.LeaseID)
		}
	}
	world.Tick()

	for i, listener := range specs {
		target := specs[(i+1)%len(specs)]
		id := vmByListener[listener.LeaseID]
		rendezvous, ok := world.VMs.RendezvousFor(id)
		if !ok {
			t.Fatalf("listener %s has no rendezvous", listener.LeaseID)
		}
		wantDevice := "/dev/zvol/fake/ws/" + target.LeaseID
		if rendezvous.WorkspaceDevice != wantDevice {
			t.Fatalf("listener %s bound %s, want %s", listener.LeaseID, rendezvous.WorkspaceDevice, wantDevice)
		}
		world.VMs.MarkBound(id)
	}
	world.Tick()

	for i, listener := range specs {
		target := specs[(i+1)%len(specs)]
		id := vmByListener[listener.LeaseID]
		authorization, ok := world.VMs.AuthorizationFor(id)
		if !ok || authorization.RequestID != "request-"+target.LeaseID {
			t.Fatalf("listener %s authorization = %#v", listener.LeaseID, authorization)
		}
		world.VMs.MarkWorkerReady(id, vm.ClockSample{})
		world.Tick()
		world.VMs.MarkHookBlocked(id, authorization.Identity)
		world.Tick()
		world.VMs.MarkReady(id, vm.ClockSample{})
	}
	world.Tick()

	for i, listener := range specs {
		target := specs[(i+1)%len(specs)]
		snapshot := world.Lease(listener.LeaseID)
		if snapshot.State != syncproto.StateReady || snapshot.ExecutionLeaseID != target.LeaseID {
			t.Fatalf("listener %s snapshot = %+v, want execution %s ready", listener.LeaseID, snapshot, target.LeaseID)
		}
	}
}

func TestReadyLeaseAllowsLongRunningJob(t *testing.T) {
	world := NewWorld(t, slots(1))
	spec := runLease("long-running")
	vmID := driveToReady(t, world, spec)

	world.Advance(24 * time.Hour)
	world.Tick()

	snapshot := world.Lease(spec.LeaseID)
	if snapshot.State != syncproto.StateReady {
		t.Fatalf("state after 24h is %s, want ready", snapshot.State)
	}
	if snapshot.VMID != vmID {
		t.Fatalf("vm after 24h is %q, want %q", snapshot.VMID, vmID)
	}
}

// TestExitQuiescesBeforeDestroy: the generation is snapshotted after the VM
// is gone, so the guest must checkpoint and flush while it is still alive;
// quiesce strictly precedes the exit-time destroy.
func TestExitQuiescesBeforeDestroy(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	vmID := driveToReady(t, world, spec)

	world.VMs.MarkExited(vm.ID(vmID), 0)
	world.Tick()
	if got := world.Lease("l1").State; got != syncproto.StateExited {
		t.Fatalf("state %s, want exited", got)
	}
	quiesceAt, destroyAt := -1, -1
	for i, entry := range world.VMs.Journal {
		switch entry {
		case "quiesce " + vmID:
			quiesceAt = i
		case "destroy " + vmID:
			destroyAt = i
		}
	}
	if quiesceAt == -1 || destroyAt == -1 || quiesceAt > destroyAt {
		t.Fatalf("journal %v: want quiesce before destroy", world.VMs.Journal)
	}
}

// TestQuiesceFailureFailsTheLease: an unquiesced workspace is ambiguous —
// dirty pages may never have reached the zvol — so the lease fails (which
// skips any seal) and the VM is still destroyed.
func TestQuiesceFailureFailsTheLease(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	vmID := driveToReady(t, world, spec)

	world.VMs.Fail = func(op string, _ vm.ID) error {
		if op == "quiesce" {
			return errors.New("guest wedged")
		}
		return nil
	}
	world.VMs.MarkExited(vm.ID(vmID), 0)
	world.Tick()

	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateFailed || !strings.Contains(snapshot.Reason, "quiesce") {
		t.Fatalf("after failed quiesce: %+v", snapshot)
	}
	if status, _ := world.VMs.Status(context.Background(), vm.ID(vmID)); status.Phase != vm.PhaseGone {
		t.Fatalf("vm still present after failed quiesce: %v", status.Phase)
	}
	for _, entry := range world.Zvols.Journal {
		if strings.HasPrefix(entry, "seal") {
			t.Fatalf("sealed an unquiesced workspace: %v", world.Zvols.Journal)
		}
	}

	// Omission is the ack for the failed lease too: its unsealed workspace
	// clone is collected, never leaked.
	deliver(world, 1)
	world.Tick()
	if world.HasLease("l1") {
		t.Fatalf("acknowledged failed lease still tracked: snapshot=%+v process=%t attached=%t journal=%v", world.Lease("l1"), world.Zvols.HasProcess("l1"), world.Zvols.ProcessAttached("l1"), world.Zvols.Journal)
	}
	if world.Zvols.HasWorkspace("l1") {
		t.Fatal("failed lease's workspace survived collection")
	}
}

func TestCacheHitClonesFromGeneration(t *testing.T) {
	world := NewWorld(t, slots(2))
	world.SeedGeneration("gen-main", 1<<20)
	spec := runLease("l1")
	spec.Workspace = syncproto.WorkspaceSpec{Generation: "gen-main"}
	driveToHookBlocked(t, world, spec)
	world.Tick()
	found := false
	for _, entry := range world.Zvols.Journal {
		if strings.Contains(entry, `ensure-workspace l1 from="gen-main"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("workspace was not cloned from gen-main: %v", world.Zvols.Journal)
	}
}

func TestCloneSourceMissingFailsLease(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	spec.Workspace = syncproto.WorkspaceSpec{Generation: "gen-absent"}
	driveToHookBlocked(t, world, spec)
	world.Tick()
	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateFailed || !strings.Contains(snapshot.Reason, "rendezvous") {
		t.Fatalf("inventory drift should fail the lease, got %+v", snapshot)
	}
}

func TestCancelAtEveryStage(t *testing.T) {
	stages := []struct {
		name  string
		drive func(t *testing.T, world *World, spec syncproto.DesiredLease)
	}{
		{"pending", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			deliver(world, 1, spec)
		}},
		{"claiming", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			deliver(world, 1, spec)
			world.Tick()
		}},
		{"assigning", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			deliver(world, 1, spec)
			world.Tick()
			bootAll(world)
			world.Tick()
		}},
		{"ready", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			driveToReady(t, world, spec)
		}},
		{"exited", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			vmID := driveToReady(t, world, spec)
			world.VMs.MarkExited(vm.ID(vmID), 1)
			world.Tick()
		}},
	}
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			world := NewWorld(t, slots(2))
			spec := runLease("l1")
			stage.drive(t, world, spec)

			cancelled := spec
			cancelled.State = syncproto.DesiredCancel
			deliver(world, 1, cancelled)
			world.Tick()
			snapshot := world.Lease("l1")
			// A lease that already exited stays exited-shaped in its
			// terminal report? No: cancel is a withdrawal; from any
			// non-terminal state it lands in cancelled.
			if snapshot.State != syncproto.StateCancelled {
				t.Fatalf("cancel at %s: state %s", stage.name, snapshot.State)
			}
			// The slot is free: no VM belongs to this lease anywhere.
			statuses, _ := world.VMs.List(context.Background())
			for _, status := range statuses {
				if status.Lease == "l1" {
					t.Fatalf("cancel at %s left vm %s bound", stage.name, status.ID)
				}
			}
			// Omission collects the workspace.
			deliver(world, 1)
			world.TickN(2)
			if world.HasLease("l1") || world.Zvols.HasWorkspace("l1") {
				t.Fatalf("cancel at %s: leftovers after ack", stage.name)
			}
		})
	}
}

func TestOmittedLiveLeaseIsWithdrawn(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	driveToReady(t, world, spec)
	// The control plane stops mentioning the live lease: full-state
	// semantics make that a withdrawal. Cancel and collection land in the
	// same tick — one pass leaves no residue at all.
	deliver(world, 1)
	world.Tick()
	if world.HasLease("l1") || world.Zvols.HasWorkspace("l1") {
		t.Fatal("withdrawn lease left residue")
	}
	statuses, _ := world.VMs.List(context.Background())
	for _, status := range statuses {
		if status.Lease == "l1" {
			t.Fatalf("withdrawn lease still holds vm %s", status.ID)
		}
	}
}

func TestRejectedSpecQuarantinesInsteadOfCollects(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	vmID := driveToReady(t, world, spec)

	// A later sync still names l1 but with a spec this hostd cannot accept
	// (version skew is the realistic cause). The lease must be neither
	// advanced nor withdrawn: a validation failure escalating to destruction
	// of live customer state is the bug the quarantine exists to prevent.
	corrupt := spec
	corrupt.Workspace = syncproto.WorkspaceSpec{Generation: "bad/../name"}
	deliver(world, 1, corrupt)
	world.TickN(3)

	if status, _ := world.VMs.Status(context.Background(), vm.ID(vmID)); status.Phase != vm.PhaseReady {
		t.Fatalf("quarantined lease's vm phase %v, want ready", status.Phase)
	}
	if !world.Zvols.HasWorkspace("l1") {
		t.Fatal("quarantined lease's workspace was collected")
	}
	if got := world.Lease("l1").State; got != syncproto.StateReady {
		t.Fatalf("quarantined lease advanced to %s", got)
	}

	// The next parseable sync resumes the lease where it left off.
	deliver(world, 1, spec)
	world.VMs.MarkExited(vm.ID(vmID), 0)
	world.Tick()
	if got := world.Lease("l1").State; got != syncproto.StateExited {
		t.Fatalf("lease did not resume after quarantine: %s", got)
	}
}

func TestRejectedSpecOnTerminalLeaseIsNotCollected(t *testing.T) {
	world := NewWorld(t, slots(1))
	spec := runLease("l1")
	// Starve the claim so the lease fails terminally; its workspace holds
	// the only copy of whatever the job left behind.
	world.Sync(syncproto.SyncResponse{Leases: []syncproto.DesiredLease{spec}})
	if _, err := world.Zvols.EnsureWorkspace(context.Background(), "l1", "", 1); err != nil {
		t.Fatal(err)
	}
	world.Tick()
	deadline, _ := agent.StateDeadline(syncproto.StateClaiming)
	world.Advance(deadline + time.Second)
	world.Tick()
	if got := world.Lease("l1").State; got != syncproto.StateFailed {
		t.Fatalf("state %s, want failed", got)
	}

	// The control plane still names l1, but with a spec this hostd cannot
	// parse. Terminal + not-in-desired must not read as an ack.
	corrupt := spec
	corrupt.Workspace = syncproto.WorkspaceSpec{Generation: "bad/../name"}
	deliver(world, 0, corrupt)
	world.TickN(2)
	if !world.HasLease("l1") {
		t.Fatal("quarantined terminal lease was forgotten")
	}
	if !world.Zvols.HasWorkspace("l1") {
		t.Fatal("quarantined terminal lease's workspace was collected")
	}

	// A genuine ack — the control plane stops naming it — collects it.
	deliver(world, 0)
	world.Tick()
	if world.HasLease("l1") || world.Zvols.HasWorkspace("l1") {
		t.Fatal("acknowledged terminal lease left residue")
	}
}

func TestQuarantineFreezesDeadlines(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	deliver(world, 1, spec)
	world.Tick()
	bootAll(world)
	world.Tick()
	if got := world.Lease("l1").State; got != syncproto.StateAssigning {
		t.Fatalf("state %s, want assigning", got)
	}

	// Version skew quarantines the lease for far longer than the assigning
	// deadline. The guest is healthy the whole time.
	corrupt := spec
	corrupt.Workspace = syncproto.WorkspaceSpec{Generation: "bad/../name"}
	deliver(world, 1, corrupt)
	deadline, _ := agent.StateDeadline(syncproto.StateAssigning)
	world.Advance(deadline * 3)
	world.TickN(2)
	if got := world.Lease("l1"); got.State != syncproto.StateAssigning {
		t.Fatalf("quarantined lease moved to %s", got.State)
	}

	// The first parseable sync must resume the lease, not execute the stale
	// deadline against a healthy job.
	deliver(world, 1, spec)
	world.Tick()
	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateAssigning {
		t.Fatalf("lease did not survive unquarantine: %+v", snapshot)
	}
	completeRendezvous(t, world, spec, snapshot.VMID)
	if got := world.Lease("l1").State; got != syncproto.StateReady {
		t.Fatalf("state %s, want ready", got)
	}
}

func TestPrepareFailureDestroysClaimedVM(t *testing.T) {
	world := NewWorld(t, slots(2))
	// Effect-then-error: the assignment lands guest-side (JIT config and
	// checkout token delivered) and the call still fails — the ambiguous
	// shape a real QMP timeout produces.
	world.VMs.FailAfter = func(op string, _ vm.ID) error {
		if op == "prepare" {
			return errAssignTimeout
		}
		return nil
	}
	spec := runLease("l1")
	deliver(world, 1, spec)
	world.Tick()
	bootAll(world)
	world.Tick()
	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateFailed || !strings.Contains(snapshot.Reason, "prepare") {
		t.Fatalf("got %+v", snapshot)
	}
	// The claimed VM was destroyed through the failure path — an ambiguous
	// assign must never strand a runner holding a live JIT config and
	// checkout token on a lease hostd reports as failed.
	if snapshot.VMID != "" {
		t.Fatal("failed lease still holds a vm")
	}
	statuses, _ := world.VMs.List(context.Background())
	for _, status := range statuses {
		if status.Lease == "l1" {
			t.Fatalf("vm %s still bound to the failed lease", status.ID)
		}
	}
}

func TestClaimingDeadlineFailsLease(t *testing.T) {
	world := NewWorld(t, slots(1))
	spec := runLease("l1")
	// Pool target zero: no warm VM will ever appear.
	world.Sync(syncproto.SyncResponse{Leases: []syncproto.DesiredLease{spec}})
	world.Tick()
	if got := world.Lease("l1").State; got != syncproto.StateClaiming {
		t.Fatalf("state %s, want claiming", got)
	}
	deadline, _ := agent.StateDeadline(syncproto.StateClaiming)
	world.Advance(deadline + time.Second)
	world.Tick()
	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateFailed || !strings.Contains(snapshot.Reason, "deadline") {
		t.Fatalf("deadline should fail the lease, got %+v", snapshot)
	}
}

func TestCrashRestartConvergesWithoutDuplicates(t *testing.T) {
	points := []struct {
		name  string
		drive func(t *testing.T, world *World, spec syncproto.DesiredLease)
	}{
		{"after-materialize", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			deliver(world, 1, spec)
			world.Tick()
		}},
		{"after-assign", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			deliver(world, 1, spec)
			world.Tick()
			bootAll(world)
			world.Tick()
		}},
		{"while-ready", func(t *testing.T, world *World, spec syncproto.DesiredLease) {
			driveToReady(t, world, spec)
		}},
	}
	for _, point := range points {
		t.Run(point.name, func(t *testing.T) {
			world := NewWorld(t, slots(2))
			spec := runLease("l1")
			point.drive(t, world, spec)

			world.Restart()
			world.Tick() // pre-sync tick must not destroy anything

			deliver(world, 1, spec)
			world.Tick() // re-materialize (idempotent) or rebind to the live VM
			bootAll(world)
			world.TickN(2)

			snapshot := world.Lease("l1")
			if snapshot.State == syncproto.StateClaiming || snapshot.State == syncproto.StatePending {
				t.Fatalf("did not converge after restart: %+v", snapshot)
			}
			if snapshot.VMID != "" {
				driveExistingToReady(t, world, spec, snapshot.VMID)
				world.VMs.MarkExited(vm.ID(snapshot.VMID), 0)
				world.Tick()
			}
			if got := world.Lease("l1").State; got != syncproto.StateExited {
				t.Fatalf("terminal state %s, want exited", got)
			}

			// No duplicate resources across the crash: the workspace was
			// created once and at most one VM ever carried this lease.
			creates := 0
			for _, entry := range world.Zvols.Journal {
				if strings.HasPrefix(entry, "ensure-workspace l1") {
					creates++
				}
			}
			if creates != 1 {
				t.Fatalf("workspace created %d times across crash", creates)
			}
			prepares := 0
			for _, entry := range world.VMs.Journal {
				if strings.HasPrefix(entry, "prepare ") {
					prepares++
				}
			}
			if prepares > 1 {
				t.Fatalf("preparation delivered %d times across crash", prepares)
			}
		})
	}
}

func TestVMDisappearanceFailsLease(t *testing.T) {
	world := NewWorld(t, slots(2))
	spec := runLease("l1")
	vmID := driveToReady(t, world, spec)
	// The hypervisor loses the VM outright (host-side crash).
	if err := world.VMs.Destroy(context.Background(), vm.ID(vmID)); err != nil {
		t.Fatal(err)
	}
	world.Tick()
	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateFailed || !strings.Contains(snapshot.Reason, "disappeared") {
		t.Fatalf("got %+v", snapshot)
	}
}

func TestReapWaitsForDependentClone(t *testing.T) {
	world := NewWorld(t, slots(2))
	world.SeedGeneration("gen-old", 1<<20)
	spec := runLease("l1")
	spec.Workspace = syncproto.WorkspaceSpec{Generation: "gen-old"}
	world.Sync(syncproto.SyncResponse{
		Leases:      []syncproto.DesiredLease{spec},
		Reap:        []string{"gen-old"},
		PoolTargets: map[string]int{class: 1},
	})
	world.Tick() // materializes the clone; reap must refuse (busy)
	if !world.Zvols.HasGeneration("gen-old") {
		t.Fatal("generation reaped while a workspace cloned it")
	}
	// Cancel and acknowledge the lease; the clone goes away, then the reap
	// lands on a later tick.
	cancelled := spec
	cancelled.State = syncproto.DesiredCancel
	world.Sync(syncproto.SyncResponse{
		Leases:      []syncproto.DesiredLease{cancelled},
		Reap:        []string{"gen-old"},
		PoolTargets: map[string]int{class: 0},
	})
	world.Tick()
	world.Sync(syncproto.SyncResponse{Reap: []string{"gen-old"}})
	world.TickN(2)
	if world.Zvols.HasGeneration("gen-old") {
		t.Fatal("acknowledged reap never executed")
	}
}

func TestPoolMaintainsAndDrains(t *testing.T) {
	world := NewWorld(t, slots(4))
	deliver(world, 3)
	world.Tick()
	bootAll(world)
	world.Tick()
	report := world.Report()
	if len(report.Slots) != 1 || report.Slots[0].Warm != 3 {
		t.Fatalf("pool did not reach target: %+v", report.Slots)
	}
	// Cordon: target zero drains the warm pool.
	deliver(world, 0)
	world.TickN(2)
	report = world.Report()
	if report.Slots[0].Warm != 0 {
		t.Fatalf("pool did not drain: %+v", report.Slots)
	}
}

func TestHostileLeaseSpecsAreRejected(t *testing.T) {
	world := NewWorld(t, slots(1))
	hostile := runLease("l1")
	hostile.LeaseID = "../../etc" // dataset traversal attempt
	other := runLease("l2")
	other.Workspace = syncproto.WorkspaceSpec{Generation: "also/../bad"}
	deliver(world, 0, hostile, other)
	if world.HasLease("../../etc") || world.HasLease("l2") {
		t.Fatal("hostile lease specs were accepted")
	}
	if got := world.Agent.Metrics().RejectedLeases.Load(); got != 2 {
		t.Fatalf("rejected %d leases, want 2", got)
	}
	world.Tick()
	if len(world.Zvols.Journal) != 0 {
		t.Fatalf("hostile specs reached the substrate: %v", world.Zvols.Journal)
	}
}

func TestZvolFaultFailsLeaseCleanly(t *testing.T) {
	world := NewWorld(t, slots(2))
	world.Zvols.Fail = func(op, id string) error {
		if op == "ensure-workspace" && id == "l1" {
			return zvol.ErrBusy
		}
		return nil
	}
	spec := runLease("l1")
	driveToHookBlocked(t, world, spec)
	world.Tick()
	snapshot := world.Lease("l1")
	if snapshot.State != syncproto.StateFailed {
		t.Fatalf("got %+v", snapshot)
	}
	if snapshot.VMID != "" {
		t.Fatal("failed lease holds a vm")
	}
}

func TestOrphanVMAndWorkspaceAreCollected(t *testing.T) {
	world := NewWorld(t, slots(2))
	// A VM claims a lease the control plane has never mentioned, and a
	// workspace volume exists for another unknown lease — crash leftovers
	// from a previous hostd life.
	if err := world.VMs.Launch(context.Background(), "vm-zombie", class); err != nil {
		t.Fatal(err)
	}
	world.VMs.AdvanceBoot("vm-zombie")
	if err := world.VMs.Prepare(context.Background(), "vm-zombie", vm.Preparation{Lease: "ghost"}); err != nil {
		t.Fatal(err)
	}
	world.VMs.MarkListening("vm-zombie")
	world.VMs.MarkAssigned("vm-zombie", vm.Assignment{RequestID: "request-ghost", RunnerName: "ghost"})
	if err := world.VMs.Rendezvous(context.Background(), "vm-zombie", vm.Rendezvous{
		Lease: "ghost", WorkspaceDevice: "/dev/zvol/ghost-workspace", WorkspaceMountpoint: "/work", ProcessDevice: "/dev/zvol/ghost-process",
	}); err != nil {
		t.Fatal(err)
	}
	world.VMs.MarkBound("vm-zombie")
	if _, err := world.Zvols.EnsureWorkspace(context.Background(), "stale", "", 1); err != nil {
		t.Fatal(err)
	}

	// Before the first sync, a restarted agent must not collect anything.
	world.Tick()
	if !world.Zvols.HasWorkspace("stale") {
		t.Fatal("collected an orphan before first sync")
	}

	deliver(world, 0)
	world.TickN(2)
	if status, _ := world.VMs.Status(context.Background(), "vm-zombie"); status.Phase != vm.PhaseGone {
		t.Fatal("zombie vm survived")
	}
	if world.Zvols.HasWorkspace("stale") || world.Zvols.HasWorkspace("ghost") {
		t.Fatal("orphan workspaces survived")
	}
}
