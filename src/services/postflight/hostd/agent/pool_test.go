package agent

import (
	"context"
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
