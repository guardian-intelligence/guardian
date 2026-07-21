package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func TestPoolReplacesStaleImageBeforeLeasing(t *testing.T) {
	ctx := context.Background()
	vms := vm.NewFake()
	vms.Images["c"] = "tank/images/old@golden"
	if err := vms.Launch(ctx, "old-listener", "c"); err != nil {
		t.Fatal(err)
	}
	if !vms.AdvanceBoot("old-listener") {
		t.Fatal("old VM did not become warm")
	}
	vms.Images["c"] = "tank/images/new@golden"

	instance, err := New(Config{
		HostID:              "host-test",
		ControlPlaneOrigin:  "https://control.invalid",
		Slots:               map[vm.Class]int{"c": 1},
		Images:              map[vm.Class]string{"c": "tank/images/new@golden"},
		CheckoutGuestOrigin: "http://198.51.100.1:8480",
	}, zvol.NewFake(), vms, "credential",
		[]byte("0123456789abcdef0123456789abcdef"),
		Options{NewID: func() string { return "replacement" }})
	if err != nil {
		t.Fatal(err)
	}
	instance.HandleSync(syncproto.SyncResponse{
		BootID: instance.bootID,
		Leases: []syncproto.DesiredLease{{
			LeaseID: "lease-1", State: syncproto.DesiredRun,
			ExecutionID: "execution-1", AttemptID: "attempt-1",
			RepositoryFullName: "acme/widget", RunnerClass: "c",
			JITConfig: "jit", ProviderRunID: 10, ProviderJobID: 11,
			ProviderRunAttempt: 1,
		}},
		PoolTargets: map[string]int{"c": 1},
	})
	instance.Tick(ctx)

	if _, prepared := vms.Preparation("old-listener"); prepared {
		t.Fatal("stale-image VM was offered to simultaneous demand")
	}
	old, err := vms.Status(ctx, "old-listener")
	if err != nil {
		t.Fatal(err)
	}
	if old.Phase != vm.PhaseGone {
		t.Fatalf("stale-image VM phase = %s, want gone", old.Phase)
	}
	replacement, err := vms.Status(ctx, "pool-replacement")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Phase != vm.PhaseBooting ||
		replacement.Image != "tank/images/new@golden" {
		t.Fatalf("replacement = %+v", replacement)
	}
}

func TestPoolReapsIdleVMFromUnconfiguredClass(t *testing.T) {
	ctx := context.Background()
	vms := vm.NewFake()
	vms.Images["legacy"] = "tank/images/legacy@golden"
	vms.Images["confidential"] = "tank/images/confidential@golden"
	if err := vms.Launch(ctx, "legacy-idle", "legacy"); err != nil {
		t.Fatal(err)
	}
	if !vms.AdvanceBoot("legacy-idle") {
		t.Fatal("legacy VM did not become warm")
	}

	instance, err := New(Config{
		HostID:              "host-test",
		ControlPlaneOrigin:  "https://control.invalid",
		Slots:               map[vm.Class]int{"confidential": 1},
		Images:              map[vm.Class]string{"confidential": "tank/images/confidential@golden"},
		CheckoutGuestOrigin: "http://198.51.100.1:8480",
	}, zvol.NewFake(), vms, "credential",
		[]byte("0123456789abcdef0123456789abcdef"),
		Options{NewID: func() string { return "replacement" }})
	if err != nil {
		t.Fatal(err)
	}
	instance.HandleSync(syncproto.SyncResponse{
		BootID: instance.bootID, PoolTargets: map[string]int{"confidential": 1},
	})
	instance.Tick(ctx)

	legacy, err := vms.Status(ctx, "legacy-idle")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Phase != vm.PhaseGone {
		t.Fatalf("legacy VM phase = %s, want gone", legacy.Phase)
	}
	replacement, err := vms.Status(ctx, "pool-replacement")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Phase != vm.PhaseBooting || replacement.Class != "confidential" {
		t.Fatalf("replacement = %+v", replacement)
	}
}

func TestVMUpdateAdvancesOnlyAssignedVM(t *testing.T) {
	ctx := context.Background()
	vms := vm.NewFake()
	vms.Images["c"] = "tank/images/current@golden"
	if err := vms.Launch(ctx, "vm-a", "c"); err != nil {
		t.Fatal(err)
	}
	if !vms.AdvanceBoot("vm-a") {
		t.Fatal("VM did not become warm")
	}
	volumes := zvol.NewFake()
	instance, err := New(Config{
		HostID: "host-test", ControlPlaneOrigin: "https://control.invalid",
		Slots: map[vm.Class]int{"c": 1}, Images: map[vm.Class]string{"c": "tank/images/current@golden"},
		CheckoutGuestOrigin: "http://198.51.100.1:8480",
	}, volumes, vms, "credential", []byte("0123456789abcdef0123456789abcdef"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	lease := syncproto.DesiredLease{
		LeaseID: "listener-1", ExecutionLeaseID: "listener-1", State: syncproto.DesiredRun,
		ExecutionID: "execution-1", AttemptID: "attempt-1", RepositoryFullName: "acme/widget",
		RunnerClass: "c", JITConfig: "jit", ProviderRunID: 10, ProviderJobID: 11,
		ProviderRunAttempt: 1, JobDisplayName: "build", Workspace: syncproto.WorkspaceSpec{SizeBytes: 1 << 20},
	}
	instance.HandleSync(syncproto.SyncResponse{
		BootID: instance.bootID, Leases: []syncproto.DesiredLease{lease}, PoolTargets: map[string]int{"c": 1},
	})
	instance.Tick(ctx) // pending -> claiming
	instance.Tick(ctx) // claim the already-warm VM
	if !vms.MarkListening("vm-a") {
		t.Fatal("prepared VM did not become listening")
	}
	instance.HandleVMUpdate(ctx, "vm-a")
	if got := instance.leases[lease.LeaseID].state; got != syncproto.StateListening {
		t.Fatalf("state = %s, want listening", got)
	}
	assignment := vm.Assignment{
		RequestID: "request-1", JobID: "runner-job-1", RunnerName: lease.LeaseID, JobDisplayName: "build",
		Identity: vm.JobIdentity{RunID: "10", RunAttempt: 1, RunnerName: lease.LeaseID, Repository: "acme/widget", WorkflowJob: "build"},
	}
	if !vms.MarkAssigned("vm-a", assignment) {
		t.Fatal("listener did not accept assignment")
	}
	vms.Fail = func(op string, _ vm.ID) error {
		if op == "list" {
			return fmt.Errorf("full pool scan reached assignment hot path")
		}
		return nil
	}
	instance.HandleVMUpdate(ctx, "vm-a")
	if got := instance.leases[lease.LeaseID].state; got != syncproto.StateBinding {
		t.Fatalf("state = %s, want binding", got)
	}
	if _, ok := vms.RendezvousFor("vm-a"); !ok {
		t.Fatal("assignment update did not dispatch rendezvous")
	}
}
