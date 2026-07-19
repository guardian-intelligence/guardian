package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var rendezvousT0 = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

func traceEvent(seq uint64, event string) rendezvousEvent {
	return rendezvousEvent{
		SchemaVersion:    rendezvousTraceSchema,
		RunID:            "run-1",
		Event:            event,
		Seq:              seq,
		Source:           "collector",
		MonotonicNS:      int64(seq) * int64(time.Millisecond),
		WallTime:         rendezvousT0.Add(time.Duration(seq) * time.Millisecond),
		ListenerLeaseID:  "warm-runner-3",
		ExecutionLeaseID: "job-42",
	}
}

func workspaceVolume() volumeEvidence {
	return volumeEvidence{
		Role:            volumeWorkspace,
		Dataset:         "tank/postflight/ws/job-42",
		Materialization: "clone",
		SnapshotGUID:    "9123456789012345678",
		Generation:      "workspace-gen-7",
		DeviceSerial:    "postflight-workspace",
	}
}

func validRendezvousTrace() []rendezvousEvent {
	exitCode := 0
	events := []rendezvousEvent{
		traceEvent(1, eventPoolReady),
		traceEvent(2, eventAssignmentObserved),
		traceEvent(3, eventJobHookBlocked),
		traceEvent(4, eventJobIdentityReported),
		traceEvent(5, eventGenerationResolved),
		traceEvent(6, eventRendezvousBound),
		traceEvent(7, eventMountsReady),
		traceEvent(8, eventClockChecked),
		traceEvent(9, eventJobHookReleased),
		traceEvent(10, eventRunnerExited),
		traceEvent(11, eventSnapshotDecided),
		traceEvent(12, eventProviderConclusionObserved),
	}
	events[0].Lane = "postflight"
	events[0].RunID = ""
	events[0].RunnerName = "warm-runner-3"
	events[0].VMID = "pool-vm-3"
	events[0].ExecutionLeaseID = ""
	events[0].Platform = &platformEvidence{
		QEMUVersion:   "10.1.0",
		KernelRelease: "6.8.0-64-generic",
		OSImageID:     "ubuntu-24.04-20260715",
		MachineType:   "pc-q35-10.1",
		CPUModel:      "AMD EPYC 9124",
		CRIUVersion:   "4.2",
	}
	events[1].JobID = 42
	events[1].RunAttempt = 3
	events[1].RunnerName = "warm-runner-3"
	events[2].JobID = 42
	events[2].RunAttempt = 3
	events[2].RunnerName = "warm-runner-3"
	events[3].Repo = "acme/repo"
	events[3].JobID = 42
	events[3].RunAttempt = 3
	events[3].RunnerName = "warm-runner-3"
	events[4].Repo = "acme/repo"
	events[4].JobID = 42
	events[4].RunAttempt = 3
	events[4].RunnerName = "warm-runner-3"
	events[4].GenerationSet = "set-7"
	events[4].Volumes = []volumeEvidence{workspaceVolume()}
	events[5].JobID = 42
	events[5].RunAttempt = 3
	events[5].RunnerName = "warm-runner-3"
	events[5].VMID = "pool-vm-3"
	events[5].GenerationSet = "set-7"
	events[5].Volumes = []volumeEvidence{workspaceVolume()}
	events[6].Volumes = []volumeEvidence{workspaceVolume()}
	events[7].Clock = &clockEvidence{
		HostBeforeUnixNS:  1_800_000_000_000_000_000,
		HostAfterUnixNS:   1_800_000_000_001_000_000,
		GuestUnixNS:       1_800_000_000_000_500_000,
		MaxSkewNS:         int64(10 * time.Millisecond),
		GuestSynchronized: true,
		Clocksource:       "kvm-clock",
	}
	events[9].ExitCode = &exitCode
	events[10].Snapshot = &snapshotEvidence{
		Policy:       "never",
		Decision:     "skip",
		Reason:       "workflow_dispatch runs collect evidence but do not create product goldens",
		RunnerExited: true,
	}
	events[11].Conclusion = "success"
	events[11].JobID = 42
	events[11].RunAttempt = 3
	return events
}

func TestValidAssignmentFirstRendezvousPasses(t *testing.T) {
	report := validateRendezvousTrace(validRendezvousTrace())
	if !report.TraceValid || report.Outcome != outcomePass {
		t.Fatalf("valid trace = %+v", report)
	}
	if report.JobID != 42 || report.RunnerName != "warm-runner-3" || report.VMID != "pool-vm-3" {
		t.Fatalf("identity = %+v", report)
	}
	if report.ListenerLeaseID != "warm-runner-3" || report.ExecutionLeaseID != "job-42" {
		t.Fatalf("routed leases = %+v", report)
	}
}

func TestCrossedListenerBindsTheActualExecutionWorkspace(t *testing.T) {
	events := validRendezvousTrace()
	for index := 1; index < len(events); index++ {
		events[index].ExecutionLeaseID = "job-99"
	}
	for _, index := range []int{4, 5, 6} {
		events[index].Volumes[0].Dataset = "tank/postflight/ws/job-99"
	}

	report := validateRendezvousTrace(events)
	if !report.TraceValid || report.Outcome != outcomePass {
		t.Fatalf("crossed listener trace = %+v", report)
	}
	if report.ListenerLeaseID != "warm-runner-3" || report.ExecutionLeaseID != "job-99" {
		t.Fatalf("crossed identity = %+v", report)
	}
}

func TestPoolReadyCannotPredictAJob(t *testing.T) {
	events := validRendezvousTrace()
	events[0].RunID = "predicted-run"
	events[0].ExecutionLeaseID = "predicted-execution"
	report := validateRendezvousTrace(events)
	if report.TraceValid ||
		!containsDetail(report.Violations, "before assignment") ||
		!containsDetail(report.Violations, "already carries customer identity") {
		t.Fatalf("owned pool listener passed: %+v", report)
	}
}

func TestWorkspaceMustBelongToExecutionLease(t *testing.T) {
	events := validRendezvousTrace()
	events[4].Volumes[0].Dataset = "tank/postflight/ws/foreign-job"
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "belongs to execution lease") {
		t.Fatalf("foreign execution workspace passed: %+v", report)
	}
}

func TestThroughReleaseValidationProvesRendezvousWithoutPostJobLifecycle(t *testing.T) {
	events := validRendezvousTrace()[:9]
	report := validateRendezvousTraceScope(events, true)
	if !report.TraceValid || report.Outcome != outcomePass {
		t.Fatalf("through-release trace = %+v", report)
	}
	if report.Events != 9 || events[5].Event != eventRendezvousBound ||
		events[5].Seq != 6 {
		t.Fatalf("step-6 evidence is not the sixth event: %+v", events[5])
	}

	full := validateRendezvousTrace(events)
	if full.TraceValid || full.Outcome != outcomeInvalid {
		t.Fatalf("partial lifecycle passed full validation: %+v", full)
	}
}

func TestRendezvousBindsOnlyTheActuallyAssignedJob(t *testing.T) {
	events := validRendezvousTrace()
	events[5].JobID = 99
	report := validateRendezvousTrace(events)
	if report.TraceValid || report.Outcome != outcomeInvalid {
		t.Fatalf("mismatched job passed: %+v", report)
	}
	if !containsDetail(report.Violations, "changes job_id") {
		t.Fatalf("violations do not identify job mismatch: %v", report.Violations)
	}
}

func TestProviderConclusionIsFromTheAssignedAttempt(t *testing.T) {
	events := validRendezvousTrace()
	events[11].RunAttempt = 4
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "changes run_attempt") {
		t.Fatalf("foreign run attempt passed: %+v", report)
	}
}

func TestWarmPoolCarriesNoCustomerVolumeBeforeAssignment(t *testing.T) {
	events := validRendezvousTrace()
	events[0].Volumes = []volumeEvidence{workspaceVolume()}
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "already carries customer volumes") {
		t.Fatalf("pre-bound pool VM passed: %+v", report)
	}
}

func TestRendezvousBindsTheResolvedGenerationTuple(t *testing.T) {
	events := validRendezvousTrace()
	events[5].Volumes[0].SnapshotGUID = "different-snapshot"
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "does not match the resolved generation tuple") {
		t.Fatalf("wrong generation tuple passed: %+v", report)
	}
}

func TestColdRendezvousExplicitlyBindsAnEmptyWorkspace(t *testing.T) {
	events := validRendezvousTrace()
	cold := workspaceVolume()
	cold.Materialization = "empty"
	cold.SnapshotGUID = ""
	cold.Generation = ""
	for _, index := range []int{4, 5, 6} {
		events[index].GenerationSet = "workspace:empty"
		events[index].Volumes = []volumeEvidence{cold}
	}

	report := validateRendezvousTrace(events)
	if !report.TraceValid || report.Outcome != outcomePass {
		t.Fatalf("explicit cold rendezvous = %+v", report)
	}
}

func TestColdRendezvousWithoutMaterializationEvidenceIsInvalid(t *testing.T) {
	events := validRendezvousTrace()
	cold := workspaceVolume()
	cold.Materialization = ""
	cold.SnapshotGUID = ""
	cold.Generation = ""
	for _, index := range []int{4, 5, 6} {
		events[index].GenerationSet = "workspace:empty"
		events[index].Volumes = []volumeEvidence{cold}
	}

	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "lacks materialization") {
		t.Fatalf("ambiguous cold rendezvous passed: %+v", report)
	}
}

func TestMaterializationMustMatchAcrossRendezvous(t *testing.T) {
	events := validRendezvousTrace()
	events[5].Volumes[0].Materialization = "empty"
	events[5].Volumes[0].SnapshotGUID = ""
	events[5].Volumes[0].Generation = ""

	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "does not match the resolved generation tuple") {
		t.Fatalf("mismatched materialization passed: %+v", report)
	}
}

func TestGuestMountsEveryBoundVolume(t *testing.T) {
	events := validRendezvousTrace()
	events[6].Volumes = nil
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "mounts_ready observed 0 volumes") {
		t.Fatalf("empty guest mounts passed: %+v", report)
	}
}

func TestMemorySnapshotWithoutWorkspaceIsInvalid(t *testing.T) {
	events := validRendezvousTrace()
	events[5].Volumes = []volumeEvidence{{
		Role:            volumeMemory,
		Dataset:         "tank/postflight/mem/job-42",
		Materialization: "clone",
		SnapshotGUID:    "8123456789012345678",
		Generation:      "memory-gen-7",
		DeviceSerial:    "postflight-memory",
	}}
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "memory snapshot without its workspace") {
		t.Fatalf("memory-only rendezvous passed: %+v", report)
	}
}

func TestMemoryRendezvousChecksClockAfterRestore(t *testing.T) {
	events := validRendezvousTrace()
	memory := volumeEvidence{
		Role:            volumeMemory,
		Dataset:         "tank/postflight/mem/job-42",
		Materialization: "clone",
		SnapshotGUID:    "8123456789012345678",
		Generation:      "memory-gen-7",
		DeviceSerial:    "postflight-memory",
	}
	for _, index := range []int{4, 5, 6} {
		events[index].Volumes = append(events[index].Volumes, memory)
	}
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "collected after restore") {
		t.Fatalf("pre-restore clock evidence passed: %+v", report)
	}

	events[7].Clock.AfterRestore = true
	report = validateRendezvousTrace(events)
	if !report.TraceValid || report.Outcome != outcomePass {
		t.Fatalf("post-restore clock evidence failed: %+v", report)
	}
}

func TestClockSkewIsAConcernNotAFailure(t *testing.T) {
	events := validRendezvousTrace()
	events[7].Clock.GuestUnixNS += int64(time.Second)
	report := validateRendezvousTrace(events)
	if !report.TraceValid || report.Outcome != outcomeConcern {
		t.Fatalf("clock concern = %+v", report)
	}
	if !containsDetail(report.Concerns, "clock_skew") {
		t.Fatalf("concerns do not identify skew: %v", report.Concerns)
	}
}

func TestPerformanceMissIsAConcernNotAFailure(t *testing.T) {
	events := validRendezvousTrace()
	issue := traceEvent(13, eventIssueObserved)
	issue.Issue = &issueEvidence{
		Code:   "slower_than_blacksmith",
		Detail: "warm execution was 8% slower on the temporary EPYC 9124 tracer",
	}
	events = append(events, issue)
	report := validateRendezvousTrace(events)
	if !report.TraceValid || report.Outcome != outcomeConcern {
		t.Fatalf("performance concern = %+v", report)
	}
}

func TestMissingToolIsInvalidRatherThanFail(t *testing.T) {
	event := traceEvent(1, eventClassified)
	event.Classification = &classificationEvidence{
		Outcome: outcomeInvalid,
		Code:    "missing_tool",
		Detail:  "cmake was absent before the supported toolchain path was exercised",
	}
	report := validateRendezvousTrace([]rendezvousEvent{event})
	if !report.TraceValid || report.Outcome != outcomeInvalid {
		t.Fatalf("missing-tool classification = %+v", report)
	}
}

func TestUnsupportedWorkloadRequiresBothSupportedFallbacks(t *testing.T) {
	event := traceEvent(1, eventClassified)
	event.Classification = &classificationEvidence{
		Outcome:                       outcomeFail,
		Code:                          "workload_unsupported",
		Detail:                        "the workload cannot run in either supported execution mode",
		TraditionalContainerAttempted: true,
	}
	report := validateRendezvousTrace([]rendezvousEvent{event})
	if report.TraceValid || report.Outcome != outcomeInvalid {
		t.Fatalf("premature FAIL classification passed: %+v", report)
	}

	event.Classification.DurableToolchainAttempted = true
	report = validateRendezvousTrace([]rendezvousEvent{event})
	if !report.TraceValid || report.Outcome != outcomeFail {
		t.Fatalf("categorical unsupported classification = %+v", report)
	}
}

func TestProtectedMainSnapshotPromotesOnlyAfterSuccess(t *testing.T) {
	events := validRendezvousTrace()[:10]
	decision := traceEvent(11, eventSnapshotDecided)
	decision.GenerationSet = "set-7"
	decision.Snapshot = &snapshotEvidence{
		Policy:            "protected_main",
		Decision:          "generate",
		TrustedRef:        true,
		RunnerExited:      true,
		AllowedProcesses:  []string{"bazel"},
		CapturedProcesses: []string{"bazel"},
	}
	sealed := traceEvent(12, eventSnapshotSealed)
	sealed.GenerationSet = "set-7"
	sealed.Volumes = []volumeEvidence{workspaceVolume()}
	sealed.Snapshot = &snapshotEvidence{
		Decision:            "generate",
		FilesystemsQuiesced: true,
	}
	conclusion := traceEvent(13, eventProviderConclusionObserved)
	conclusion.Conclusion = "success"
	conclusion.JobID = 42
	conclusion.RunAttempt = 3
	promoted := traceEvent(14, eventGenerationPromoted)
	promoted.GenerationSet = "set-7"
	events = append(events, decision, sealed, conclusion, promoted)

	report := validateRendezvousTrace(events)
	if !report.TraceValid || report.Outcome != outcomePass {
		t.Fatalf("protected-main promotion = %+v", report)
	}

	events[len(events)-2].Conclusion = "failure"
	report = validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "requires the attempt-scoped success") {
		t.Fatalf("failed provider conclusion promoted: %+v", report)
	}
}

func TestSnapshotSealsTheWholeBoundGenerationSet(t *testing.T) {
	events := validRendezvousTrace()[:10]
	toolchain := volumeEvidence{
		Role:            volumeToolchain,
		Dataset:         "tank/postflight/tools/acme",
		Materialization: "clone",
		SnapshotGUID:    "7123456789012345678",
		Generation:      "toolchain-gen-3",
		DeviceSerial:    "postflight-toolchain",
	}
	for _, index := range []int{4, 5, 6} {
		events[index].Volumes = append(events[index].Volumes, toolchain)
	}
	decision := traceEvent(11, eventSnapshotDecided)
	decision.GenerationSet = "set-7"
	decision.Snapshot = &snapshotEvidence{
		Policy:       "protected_main",
		Decision:     "generate",
		TrustedRef:   true,
		RunnerExited: true,
	}
	sealed := traceEvent(12, eventSnapshotSealed)
	sealed.GenerationSet = "set-7"
	sealed.Volumes = []volumeEvidence{workspaceVolume()}
	sealed.Snapshot = &snapshotEvidence{
		Decision:            "generate",
		FilesystemsQuiesced: true,
	}
	conclusion := traceEvent(13, eventProviderConclusionObserved)
	conclusion.JobID = 42
	conclusion.RunAttempt = 3
	conclusion.Conclusion = "success"
	discarded := traceEvent(14, eventGenerationDiscarded)
	discarded.GenerationSet = "set-7"
	events = append(events, decision, sealed, conclusion, discarded)

	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "did not seal bound toolchain volume") {
		t.Fatalf("partial generation snapshot passed: %+v", report)
	}
}

func TestSnapshotNeverIncludesRunnerMemory(t *testing.T) {
	events := validRendezvousTrace()[:10]
	decision := traceEvent(11, eventSnapshotDecided)
	decision.GenerationSet = "set-7"
	decision.Snapshot = &snapshotEvidence{
		Policy:               "benchmark_seed",
		Decision:             "generate",
		TrustedRef:           true,
		RunnerExited:         true,
		RunnerMemoryIncluded: true,
	}
	events = append(events, decision)
	report := validateRendezvousTrace(events)
	if report.TraceValid || !containsDetail(report.Violations, "runner memory") {
		t.Fatalf("runner-memory snapshot passed: %+v", report)
	}
}

func TestTraceReaderRejectsUnknownFieldsAndMultipleValues(t *testing.T) {
	base := `{"schema_version":1,"run_id":"r","event":"classified","seq":1,"source":"collector","monotonic_ns":1,"wall_time":"2026-07-19T12:00:00Z"`
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
