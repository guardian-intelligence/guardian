package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
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

	unassigned := agent.newTraceEvent(record, "assignment_observed")
	if unassigned.RunID != "" || unassigned.ExecutionLeaseID != "" {
		t.Fatalf("unassigned trace carries job ownership: %+v", unassigned)
	}
	record.assignment = &vm.Assignment{RequestID: "request-1"}
	record.execution = &syncproto.DesiredLease{
		ExecutionLeaseID: "execution-2", ProviderRunID: 123,
	}
	assigned := agent.newTraceEvent(record, "assignment_observed")
	if assigned.RunID != "123" || assigned.ExecutionLeaseID != "execution-2" ||
		assigned.ListenerLeaseID != "listener-1" {
		t.Fatalf("assignment trace lacks routed identity: %+v", assigned)
	}
}

func TestBootstrapOriginTimingRemainsUnownedAfterAssignment(t *testing.T) {
	agent := &Agent{
		cfg: Config{HostID: "host-1"}, started: time.Now().Add(-time.Second),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	record := &lease{
		spec: syncproto.DesiredLease{LeaseID: "listener-1"}, vmID: "vm-1",
		assignment: &vm.Assignment{RequestID: "request-1"},
		execution: &syncproto.DesiredLease{
			ExecutionLeaseID: "execution-2", ProviderRunID: 123,
		},
	}
	var observed traceEvent
	agent.cfg.TraceDir = t.TempDir()
	agent.traceFiles = map[string]*os.File{}
	agent.appendOriginTiming(record, []vm.TimingPoint{{
		Event: "qemu_started", Source: "hostd-qemu", BootID: "boot-1",
		Sequence: 2, MonotonicNS: 20, UnixNS: time.Now().UnixNano(),
	}})
	raw, err := os.ReadFile(filepath.Join(agent.cfg.TraceDir, "listener-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &observed); err != nil {
		t.Fatal(err)
	}
	if observed.RunID != "" || observed.ExecutionLeaseID != "" ||
		observed.RunnerName != "listener-1" || observed.VMID != "vm-1" {
		t.Fatalf("bootstrap timing carries job ownership: %+v", observed)
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

func TestFailureTraceIncludesReasonAndVMDestroy(t *testing.T) {
	agent := testAgent(t, "http://control.invalid")
	agent.cfg.TraceDir = t.TempDir()
	record := &lease{
		spec: syncproto.DesiredLease{LeaseID: "listener-1"},
		vmID: "vm-1",
	}
	agent.failLease(context.Background(), record, "worker namespace entry failed")

	file, err := os.Open(filepath.Join(agent.cfg.TraceDir, "listener-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []traceEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event traceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Event != "lease_failed" ||
		events[0].FailureReason != "worker namespace entry failed" ||
		events[1].Event != "vm_destroy_started" || events[2].Event != "vm_destroy_completed" {
		t.Fatalf("failure trace = %+v", events)
	}
}
