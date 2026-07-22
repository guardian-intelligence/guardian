package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var rendezvousT0 = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

type traceBuilder struct {
	events    []rendezvousEvent
	originSeq map[string]uint64
}

func (b *traceBuilder) add(name, source string) *rendezvousEvent {
	b.originSeq[source]++
	seq := uint64(len(b.events) + 1)
	event := rendezvousEvent{
		SchemaVersion: rendezvousTraceSchema, RunID: "1001", Event: name,
		Seq: seq, Source: source, BootID: "boot-1", OriginSeq: b.originSeq[source],
		MonotonicNS: int64(b.originSeq[source]) * int64(time.Millisecond),
		WallTime:    rendezvousT0.Add(time.Duration(seq) * time.Millisecond),
		Repo:        "acme/repo", JobID: 42, CheckRunID: 4200, RunAttempt: 3,
		RunnerName: "warm-runner-3", RequestID: "request-7", RunnerJobID: "runner-job-9",
		VMID: "pool-vm-3", MemberID: "pool-member-3", AssignmentID: "job-42",
	}
	if preAssignmentEvents[name] {
		event.RunID, event.Repo, event.RequestID, event.RunnerJobID, event.AssignmentID = "", "", "", "", ""
		event.JobID, event.CheckRunID, event.RunAttempt = 0, 0, 0
	}
	b.events = append(b.events, event)
	return &b.events[len(b.events)-1]
}

func workspaceVolume(warm, bound bool) volumeEvidence {
	volume := volumeEvidence{Role: volumeWorkspace, Dataset: "tank/postflight/ws/job-42", Materialization: "empty"}
	if warm {
		volume.Materialization, volume.Generation, volume.SnapshotGUID = "clone", "generation-7", "9123456789012345678"
	}
	if bound {
		volume.DeviceSerial = "workspace"
	}
	return volume
}

func processVolume(warm, bound bool) volumeEvidence {
	volume := volumeEvidence{Role: volumeProcess, Dataset: "tank/postflight/process-state/ws/job-42", Materialization: "empty"}
	if warm {
		volume.Materialization, volume.Generation, volume.SnapshotGUID = "clone", "generation-7", "8123456789012345678"
	}
	if bound {
		volume.DeviceSerial = "process"
	}
	return volume
}

func toolVolume(warm, bound bool) volumeEvidence {
	volume := volumeEvidence{Role: volumeTool, Dataset: "tank/postflight/tool-state/ws/job-42", Materialization: "empty"}
	if warm {
		volume.Materialization, volume.Generation, volume.SnapshotGUID = "clone", "generation-7", "7123456789012345678"
	}
	if bound {
		volume.DeviceSerial = "tool"
	}
	return volume
}

func validRendezvousTrace(warm, full bool) []rendezvousEvent {
	b := &traceBuilder{originSeq: map[string]uint64{}}
	for _, item := range [][2]string{
		{eventVMLaunchStarted, "hostd-qemu"}, {eventQEMUStarted, "hostd-qemu"},
		{eventGuestHelloObserved, "hostd-qemu"}, {eventListenerPrepareStarted, "hostd-qemu"},
		{eventListenerPrepareSent, "hostd-qemu"}, {eventListenerPrepareReceived, "guestd"},
		{eventRunnerRegistered, "guestd"}, {eventPoolReady, "hostd-agent"},
		{eventAssignmentUpdateReceived, "hostd-agent"}, {eventHostAssignmentObserved, "hostd-qemu"},
		{eventRunnerAssignmentReceived, "runner-listener"}, {eventGuestAssignmentReceived, "guestd"},
		{eventVsockAssignmentReceived, "hostd-vsock"},
		{eventGuestAssignmentPublished, "guestd"}, {eventRunnerWorkerGateEntered, "guestd"},
		{eventAssignmentObserved, "hostd-agent"}, {eventGenerationMaterializationStarted, "hostd-agent"},
		{eventGenerationResolved, "hostd-agent"}, {eventRendezvousDispatched, "hostd-agent"},
		{eventQMPRendezvousStarted, "hostd-qemu"}, {eventQMPConnected, "hostd-qemu"},
		{eventWorkspaceDeviceAttached, "hostd-qemu"}, {eventToolDeviceAttached, "hostd-qemu"},
		{eventProcessDeviceAttached, "hostd-qemu"},
		{eventGuestRendezvousSent, "hostd-qemu"}, {eventGuestRendezvousReceived, "guestd"},
		{eventMountConvergenceStarted, "guestd"}, {eventMountConvergenceCompleted, "guestd"},
	} {
		b.add(item[0], item[1])
	}
	if warm {
		b.add(eventCRIURestoreStarted, "guestd")
		b.add(eventRestoreBoundaryStarted, "guestd")
		b.add(eventRestoreBoundaryCompleted, "guestd")
		b.add(eventRestoreVersionStarted, "guestd")
		b.add(eventRestoreVersionCompleted, "guestd")
		b.add(eventRestoreDigestStarted, "guestd")
		b.add(eventRestoreDigestCompleted, "guestd")
		b.add(eventRestoreCRIUStarted, "guestd")
		b.add(eventRestoreCRIUCompleted, "guestd")
		b.add(eventCRIURestoreCompleted, "guestd")
	} else {
		b.add(eventColdCapsuleStartStarted, "guestd")
		b.add(eventColdCapsuleStartCompleted, "guestd")
	}
	for _, item := range [][2]string{
		{eventGenerationRestoreCompleted, "guestd"}, {eventMountsReady, "hostd-agent"},
		{eventClockChecked, "hostd-agent"}, {eventWorkerAuthorizationSent, "hostd-agent"},
		{eventRunnerWorkerReleased, "guestd"}, {eventRunnerWorkerGateCompleted, "guestd"},
		{eventRunnerWorkerExecStarted, "guestd"},
		{eventJobHookValidated, "guestd"},
		{eventCustomerStepsReleased, "guestd"}, {eventJobHookReleased, "hostd-agent"},
	} {
		b.add(item[0], item[1])
	}
	b.events[7].Platform = &platformEvidence{
		QEMUVersion: "11.0.2", KernelRelease: "6.8.0-134-generic",
		OSImageID: "noble-1f782d295df9-g07c10dda9277", MachineType: "pc-q35-11.0",
		CPUModel: "EPYC-v4", CRIUVersion: "4.2",
	}
	resolved := []volumeEvidence{workspaceVolume(warm, false), toolVolume(warm, false), processVolume(warm, false)}
	bound := []volumeEvidence{workspaceVolume(warm, true), toolVolume(warm, true), processVolume(warm, true)}
	for index := range b.events {
		switch b.events[index].Event {
		case eventGenerationResolved:
			b.events[index].GenerationSet = generationSetForTest(warm)
			b.events[index].Volumes = resolved
		case eventRendezvousDispatched, eventMountsReady:
			b.events[index].GenerationSet = generationSetForTest(warm)
			b.events[index].Volumes = bound
			if b.events[index].Event == eventMountsReady {
				if warm {
					b.events[index].Restore = &restoreEvidence{Outcome: "restored"}
				} else {
					b.events[index].Restore = &restoreEvidence{Outcome: "not-requested"}
				}
			}
		case eventClockChecked:
			b.events[index].GenerationSet = generationSetForTest(warm)
			b.events[index].Clock = &clockEvidence{
				HostBeforeUnixNS: 1_800_000_000_000_000_000,
				HostAfterUnixNS:  1_800_000_000_001_000_000,
				GuestUnixNS:      1_800_000_000_000_500_000,
				MaxSkewNS:        int64(10 * time.Millisecond), GuestSynchronized: true,
				Clocksource: "kvm-clock", AfterRestore: warm,
			}
		}
	}
	if !full {
		return b.events
	}
	for _, item := range [][2]string{
		{eventRunnerExited, "guestd"}, {eventRunnerExitObserved, "hostd-agent"},
		{eventCheckpointStarted, "hostd-agent"}, {eventQuiesceRPCStarted, "hostd-qemu"},
		{eventQuiesceReceived, "guestd"}, {eventQuiesceMountsChecked, "guestd"},
		{eventCheckpointDumpStarted, "guestd"},
		{eventCheckpointCapsulePrepareStarted, "guestd"}, {eventCheckpointCapsulePrepareCompleted, "guestd"},
		{eventCheckpointVersionStarted, "guestd"}, {eventCheckpointVersionCompleted, "guestd"},
		{eventCheckpointCRIUDumpStarted, "guestd"}, {eventCheckpointCRIUDumpCompleted, "guestd"},
		{eventCheckpointDigestStarted, "guestd"}, {eventCheckpointDigestCompleted, "guestd"},
		{eventCheckpointDumpCompleted, "guestd"},
		{eventFilesystemSyncStarted, "guestd"}, {eventFilesystemSyncCompleted, "guestd"},
		{eventQuiesceRPCCompleted, "hostd-qemu"}, {eventCheckpointCompleted, "hostd-agent"},
		{eventVMDestroyStarted, "hostd-agent"}, {eventVMDestroyCompleted, "hostd-agent"},
		{eventSnapshotSealStarted, "hostd-agent"}, {eventSnapshotSealCompleted, "hostd-agent"},
	} {
		event := b.add(item[0], item[1])
		event.GenerationSet = generationSetForTest(warm)
		if item[0] == eventCheckpointCompleted || item[0] == eventSnapshotSealStarted || item[0] == eventSnapshotSealCompleted {
			event.Checkpoint = &checkpointEvidence{Digest: "sha256:abc", Version: "criu-4.2"}
		}
	}
	return b.events
}

func generationSetForTest(warm bool) string {
	if warm {
		return "workspace:generation-7:9123456789012345678,tool:generation-7:7123456789012345678,process:generation-7:8123456789012345678"
	}
	return "workspace:empty,tool:empty,process:empty"
}

func TestValidColdRendezvousThroughRelease(t *testing.T) {
	report := validateRendezvousTraceScope(validRendezvousTrace(false, false), true)
	if !report.TraceValid || report.Outcome != outcomePass || report.RestoreMode != "cold" {
		t.Fatalf("valid cold trace = %+v", report)
	}
	if report.DurationsNS["generation_materialization"] <= 0 || report.DurationsNS["cold_capsule_start"] <= 0 ||
		report.DurationsNS["vsock_to_assignment_update"] <= 0 {
		t.Fatalf("high-resolution durations = %+v", report.DurationsNS)
	}
}

func TestValidWarmRendezvousAndCheckpointPasses(t *testing.T) {
	report := validateRendezvousTrace(validRendezvousTrace(true, true))
	if !report.TraceValid || report.Outcome != outcomePass || report.RestoreMode != "warm" {
		t.Fatalf("valid warm trace = %+v", report)
	}
	for _, duration := range []string{
		"criu_restore", "restore_boundary", "restore_version_validation", "restore_digest_validation", "restore_criu",
		"qmp_to_workspace_attach", "workspace_to_tool_attach", "tool_to_process_attach",
		"process_attach_to_guest_dispatch",
		"worker_gate", "checkpoint_dump", "checkpoint_capsule_prepare", "checkpoint_version",
		"checkpoint_criu_dump", "checkpoint_digest", "snapshot_seal",
	} {
		if report.DurationsNS[duration] <= 0 {
			t.Fatalf("missing %s duration: %+v", duration, report.DurationsNS)
		}
	}
}

func TestTerminalRestoreRetryEventsRemainValidEvidence(t *testing.T) {
	b := &traceBuilder{originSeq: map[string]uint64{}}
	b.add(eventPoolReady, "hostd-agent").Platform = &platformEvidence{
		QEMUVersion: "11.0.2", KernelRelease: "6.8.0", OSImageID: "ubuntu-24.04",
		MachineType: "pc-q35-11.0", CPUModel: "EPYC-v4", CRIUVersion: "4.2",
	}
	b.add(eventAssignmentUpdateReceived, "hostd-agent")
	for attempt := 0; attempt < 2; attempt++ {
		b.add(eventRestoreCleanupStarted, "guestd")
		b.add(eventRestoreCleanupCompleted, "guestd")
		b.add(eventColdCapsuleStartStarted, "guestd")
		b.add(eventColdCapsuleStartFailed, "guestd")
	}
	failed := b.add(eventAssignmentFailedClosed, "hostd-agent")
	failed.FailureReason = "cold capsule start failed"
	failed.Restore = &restoreEvidence{
		Outcome: "unsafe", ProcessInvalidated: true,
		FailureClass: "cleanup", FailureCode: "cold-capsule-start",
	}
	report := validateRendezvousTraceScope(b.events, true)
	if !report.TraceValid || report.Outcome != outcomeInvalid {
		t.Fatalf("terminal retry trace = %+v", report)
	}
}

func TestProviderAbortedAssignmentIsValidTerminalEvidence(t *testing.T) {
	b := &traceBuilder{originSeq: map[string]uint64{}}
	b.add(eventPoolReady, "hostd-agent").Platform = &platformEvidence{
		QEMUVersion: "11.0.2", KernelRelease: "6.8.0", OSImageID: "ubuntu-24.04",
		MachineType: "pc-q35-11.0", CPUModel: "EPYC-v4", CRIUVersion: "4.2",
	}
	b.add(eventAssignmentUpdateReceived, "hostd-agent")
	b.add(eventVMDestroyStarted, "hostd-agent")
	b.add(eventVMDestroyCompleted, "hostd-agent")
	aborted := b.add(eventAssignmentAborted, "hostd-agent")
	aborted.FailureReason = "assignment withdrawn by provider"
	report := validateRendezvousTraceScope(b.events, true)
	if !report.TraceValid || report.Outcome != outcomeInvalid {
		t.Fatalf("provider-aborted trace = %+v", report)
	}
}

func TestRecoverableProcessRestoreFallsBackColdWithoutDiscardingWorkspace(t *testing.T) {
	events := validRendezvousTrace(true, false)
	filtered := make([]rendezvousEvent, 0, len(events)+3)
	for _, event := range events {
		if event.Event == eventRestoreCRIUCompleted || event.Event == eventCRIURestoreCompleted {
			continue
		}
		if event.Event == eventGenerationRestoreCompleted {
			for _, name := range []string{
				eventGenerationRestoreFailed, eventRestoreCleanupStarted, eventRestoreCleanupCompleted,
				eventColdCapsuleStartStarted, eventColdCapsuleStartCompleted,
			} {
				inserted := event
				inserted.Event = name
				inserted.Restore = nil
				filtered = append(filtered, inserted)
			}
		}
		if event.Event == eventMountsReady {
			event.Restore = &restoreEvidence{
				Outcome: "cold-fallback", ProcessInvalidated: true,
				FailureClass: "incompatible", FailureCode: "criu-rejected",
			}
		}
		if event.Event == eventClockChecked {
			event.Clock.AfterRestore = false
		}
		filtered = append(filtered, event)
	}
	resequenceTrace(filtered)
	report := validateRendezvousTraceScope(filtered, true)
	if !report.TraceValid || report.Outcome != outcomePass || report.WorkspaceMode != "warm" || report.ProcessMode != "cold-fallback" {
		t.Fatalf("recoverable restore trace = %+v", report)
	}
	if report.RestoreFailureClass != "incompatible" || report.RestoreFailureCode != "criu-rejected" || report.DurationsNS["restore_cleanup"] <= 0 {
		t.Fatalf("fallback evidence = %+v", report)
	}
}

func resequenceTrace(events []rendezvousEvent) {
	origin := map[string]uint64{}
	for index := range events {
		origin[events[index].Source]++
		events[index].Seq = uint64(index + 1)
		events[index].OriginSeq = origin[events[index].Source]
		events[index].MonotonicNS = int64(origin[events[index].Source]) * int64(time.Millisecond)
		events[index].WallTime = rendezvousT0.Add(time.Duration(index+1) * time.Millisecond)
	}
}

func TestAdoptedWarmVMDoesNotRequireInMemoryLaunchTiming(t *testing.T) {
	events := validRendezvousTrace(false, false)
	filtered := events[:0]
	for _, event := range events {
		if event.Event != eventVMLaunchStarted && event.Event != eventQEMUStarted {
			filtered = append(filtered, event)
		}
	}
	for index := range filtered {
		filtered[index].Seq = uint64(index + 1)
	}
	report := validateRendezvousTraceScope(filtered, true)
	if !report.TraceValid || report.Outcome != outcomePass {
		t.Fatalf("adopted VM trace = %+v", report)
	}
}

func TestBootstrapCannotPredictCustomerIdentity(t *testing.T) {
	events := validRendezvousTrace(false, false)
	events[0].RunID = "1001"
	report := validateRendezvousTraceScope(events, true)
	if report.TraceValid || !containsDetail(report.Violations, "pre-assignment") {
		t.Fatalf("owned bootstrap passed: %+v", report)
	}
}

func TestCrossedListenerBindsActualExecutionVolumes(t *testing.T) {
	events := validRendezvousTrace(true, false)
	for index := range events {
		if preAssignmentEvents[events[index].Event] {
			continue
		}
		events[index].AssignmentID = "job-99"
		for volumeIndex := range events[index].Volumes {
			events[index].Volumes[volumeIndex].Dataset = strings.Replace(events[index].Volumes[volumeIndex].Dataset, "job-42", "job-99", 1)
		}
	}
	report := validateRendezvousTraceScope(events, true)
	if !report.TraceValid || report.AssignmentID != "job-99" || report.MemberID != "pool-member-3" {
		t.Fatalf("crossed assignment trace = %+v", report)
	}
}

func TestWorkspaceAndProcessMustBeOneGeneration(t *testing.T) {
	events := validRendezvousTrace(true, false)
	for index := range events {
		for volumeIndex := range events[index].Volumes {
			if events[index].Volumes[volumeIndex].Role == volumeProcess {
				events[index].Volumes[volumeIndex].Generation = "foreign-generation"
			}
		}
	}
	report := validateRendezvousTraceScope(events, true)
	if report.TraceValid || !containsDetail(report.Violations, "does not belong to the workspace generation") {
		t.Fatalf("split generation passed: %+v", report)
	}
}

func TestWarmRendezvousRequiresCRIURestoreEvidence(t *testing.T) {
	events := validRendezvousTrace(true, false)
	filtered := events[:0]
	for _, event := range events {
		if event.Event != eventCRIURestoreCompleted {
			filtered = append(filtered, event)
		}
	}
	for index := range filtered {
		filtered[index].Seq = uint64(index + 1)
	}
	report := validateRendezvousTraceScope(filtered, true)
	if report.TraceValid || !containsDetail(report.Violations, eventCRIURestoreCompleted) {
		t.Fatalf("partial CRIU restore passed: %+v", report)
	}
}

func TestClockSkewIsAConcernNotFailure(t *testing.T) {
	events := validRendezvousTrace(true, false)
	for index := range events {
		if events[index].Event == eventClockChecked {
			events[index].Clock.GuestUnixNS += int64(time.Second)
		}
	}
	report := validateRendezvousTraceScope(events, true)
	if !report.TraceValid || report.Outcome != outcomeConcern || !containsDetail(report.Concerns, "clock_skew") {
		t.Fatalf("clock concern = %+v", report)
	}
}

func TestIntegrityRestoreFailureIsValidEvidenceForAnInvalidRun(t *testing.T) {
	b := &traceBuilder{originSeq: map[string]uint64{}}
	b.add(eventPoolReady, "hostd-agent").Platform = &platformEvidence{
		QEMUVersion: "11.0.2", KernelRelease: "6.8.0", OSImageID: "ubuntu-24.04",
		MachineType: "pc-q35-11.0", CPUModel: "EPYC-v4", CRIUVersion: "4.2",
	}
	b.add(eventAssignmentUpdateReceived, "hostd-agent")
	failed := b.add(eventAssignmentFailedClosed, "hostd-agent")
	failed.FailureReason = "checkpoint digest mismatch"
	failed.Restore = &restoreEvidence{
		Outcome: "unsafe", ProcessInvalidated: true,
		FailureClass: "integrity", FailureCode: "artifact-digest",
	}
	report := validateRendezvousTraceScope(b.events, true)
	if !report.TraceValid || report.Outcome != outcomeInvalid || report.RestoreOutcome != "unsafe" {
		t.Fatalf("integrity failure trace = %+v", report)
	}
	b.add(eventCustomerStepsReleased, "guestd")
	report = validateRendezvousTraceScope(b.events, true)
	if report.TraceValid || !containsDetail(report.Violations, "released customer steps") {
		t.Fatalf("fail-closed release passed: %+v", report)
	}
}

func TestClockDomainCannotMoveBackward(t *testing.T) {
	events := validRendezvousTrace(false, false)
	events[1].OriginSeq = events[0].OriginSeq
	report := validateRendezvousTraceScope(events, true)
	if report.TraceValid || !containsDetail(report.Violations, "origin_seq moved") {
		t.Fatalf("clock regression passed: %+v", report)
	}
}

func TestThroughReleaseRejectsMissingPostJobOnlyInFullMode(t *testing.T) {
	events := validRendezvousTrace(false, false)
	if report := validateRendezvousTraceScope(events, true); !report.TraceValid {
		t.Fatalf("through-release trace = %+v", report)
	}
	if report := validateRendezvousTrace(events); report.TraceValid || !containsDetail(report.Violations, eventCheckpointCompleted) {
		t.Fatalf("partial lifecycle passed full validation: %+v", report)
	}
}

func TestUnsupportedWorkloadRequiresBothSupportedFallbacks(t *testing.T) {
	b := &traceBuilder{originSeq: map[string]uint64{}}
	event := b.add(eventClassified, "collector")
	event.Classification = &classificationEvidence{
		Outcome: outcomeFail, Code: "workload_unsupported",
		Detail:                        "the workload cannot run in either supported execution mode",
		TraditionalContainerAttempted: true,
	}
	report := validateRendezvousTrace(b.events)
	if report.TraceValid || report.Outcome != outcomeInvalid {
		t.Fatalf("premature FAIL classification passed: %+v", report)
	}
	event.Classification.DurableToolchainAttempted = true
	report = validateRendezvousTrace(b.events)
	if !report.TraceValid || report.Outcome != outcomeFail {
		t.Fatalf("categorical unsupported classification = %+v", report)
	}
}

func TestTraceReaderRejectsUnknownFieldsAndMultipleValues(t *testing.T) {
	base := `{"schema_version":7,"run_id":"r","event":"classified","seq":1,"source":"collector","boot_id":"boot","origin_seq":1,"monotonic_ns":1,"wall_time":"2026-07-21T12:00:00Z"`
	if _, err := readRendezvousTrace(strings.NewReader(base + `,"surprise":true}`)); err == nil {
		t.Fatal("unknown trace field was accepted")
	}
	if _, err := readRendezvousTrace(bytes.NewBufferString(base + `} {}`)); err == nil {
		t.Fatal("multiple JSON values on one line were accepted")
	}
}

func containsDetail(items []string, substring string) bool {
	for _, item := range items {
		if strings.Contains(item, substring) {
			return true
		}
	}
	return false
}
