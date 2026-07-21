package agent

import (
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func TestPoolTraceIsUnownedUntilAssignment(t *testing.T) {
	agent := &Agent{
		cfg:     Config{HostID: "host-1"},
		started: time.Now().Add(-time.Second),
	}
	record := &lease{spec: syncproto.DesiredLease{
		LeaseID:            "listener-1",
		ExecutionLeaseID:   "execution-2",
		ProviderRunID:      123,
		ProviderJobID:      456,
		ProviderRunAttempt: 1,
	}}

	pool := agent.newTraceEvent(record, "pool_ready")
	if pool.RunID != "" || pool.ExecutionLeaseID != "" ||
		pool.ListenerLeaseID != "listener-1" {
		t.Fatalf("pool trace carries job ownership: %+v", pool)
	}

	assigned := agent.newTraceEvent(record, "assignment_observed")
	if assigned.RunID != "123" || assigned.ExecutionLeaseID != "execution-2" ||
		assigned.ListenerLeaseID != "listener-1" {
		t.Fatalf("assignment trace lacks routed identity: %+v", assigned)
	}
}

func TestColdWorkspaceTraceNamesEmptyMaterialization(t *testing.T) {
	record := &lease{
		volume:        zvol.WorkspaceVolume{Name: "tank/postflight/ws/lease-1"},
		processVolume: zvol.ProcessVolume{Name: "tank/postflight/process-state/ws/lease-1"},
	}

	if got := generationSet(record); got != "workspace:empty,process:empty" {
		t.Fatalf("generationSet() = %q, want paired empty generations", got)
	}
	volumes := traceVolumes(record, true)
	if len(volumes) != 2 {
		t.Fatalf("traceVolumes() returned %d volumes, want 2", len(volumes))
	}
	volume := volumes[0]
	if volume.Materialization != "empty" || volume.Generation != "" ||
		volume.SnapshotGUID != "" || volume.DeviceSerial != "workspace" {
		t.Fatalf("cold volume = %+v", volume)
	}
}

func TestWarmWorkspaceTraceNamesCloneMaterialization(t *testing.T) {
	record := &lease{volume: zvol.WorkspaceVolume{
		Name:               "tank/postflight/ws/lease-1",
		Source:             "generation-1",
		SourceSnapshotGUID: "123456789",
	}, processVolume: zvol.ProcessVolume{
		Name:               "tank/postflight/process-state/ws/lease-1",
		Source:             "generation-1",
		SourceSnapshotGUID: "987654321",
	}}

	if got := generationSet(record); got != "workspace:generation-1:123456789,process:generation-1:987654321" {
		t.Fatalf("generationSet() = %q", got)
	}
	volume := traceVolumes(record, false)[0]
	if volume.Materialization != "clone" || volume.Generation != "generation-1" ||
		volume.SnapshotGUID != "123456789" || volume.DeviceSerial != "" {
		t.Fatalf("warm volume = %+v", volume)
	}
}
