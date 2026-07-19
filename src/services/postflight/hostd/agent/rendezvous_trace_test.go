package agent

import (
	"testing"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func TestColdWorkspaceTraceNamesEmptyMaterialization(t *testing.T) {
	record := &lease{volume: zvol.WorkspaceVolume{Name: "tank/postflight/ws/lease-1"}}

	if got := generationSet(record); got != "workspace:empty" {
		t.Fatalf("generationSet() = %q, want workspace:empty", got)
	}
	volumes := traceVolumes(record, true)
	if len(volumes) != 1 {
		t.Fatalf("traceVolumes() returned %d volumes, want 1", len(volumes))
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
	}}

	if got := generationSet(record); got != "workspace:generation-1:123456789" {
		t.Fatalf("generationSet() = %q", got)
	}
	volume := traceVolumes(record, false)[0]
	if volume.Materialization != "clone" || volume.Generation != "generation-1" ||
		volume.SnapshotGUID != "123456789" || volume.DeviceSerial != "" {
		t.Fatalf("warm volume = %+v", volume)
	}
}
