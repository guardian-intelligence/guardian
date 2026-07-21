package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const rendezvousTraceSchema = 5

const (
	eventVMLaunchStarted                   = "vm_launch_started"
	eventQEMUStarted                       = "qemu_started"
	eventGuestHelloObserved                = "guest_hello_observed"
	eventListenerPrepareStarted            = "listener_prepare_started"
	eventListenerPrepareSent               = "listener_prepare_sent"
	eventListenerPrepareReceived           = "listener_prepare_received"
	eventRunnerRegistered                  = "runner_registered"
	eventPoolReady                         = "pool_ready"
	eventRunnerAssignmentReceived          = "runner_assignment_received"
	eventGuestAssignmentReceived           = "guest_assignment_received"
	eventGuestAssignmentPublished          = "guest_assignment_published"
	eventVsockAssignmentReceived           = "vsock_assignment_received"
	eventHostAssignmentObserved            = "host_assignment_observed"
	eventAssignmentUpdateReceived          = "assignment_update_received"
	eventAssignmentObserved                = "assignment_observed"
	eventGenerationMaterializationStarted  = "generation_materialization_started"
	eventGenerationResolved                = "generation_resolved"
	eventQMPRendezvousStarted              = "qmp_rendezvous_started"
	eventQMPConnected                      = "qmp_connected"
	eventWorkspaceDeviceAttached           = "workspace_device_attached"
	eventProcessDeviceAttached             = "process_device_attached"
	eventGuestRendezvousSent               = "guest_rendezvous_sent"
	eventRendezvousDispatched              = "rendezvous_dispatched"
	eventGuestRendezvousReceived           = "guest_rendezvous_received"
	eventMountConvergenceStarted           = "mount_convergence_started"
	eventMountConvergenceCompleted         = "mount_convergence_completed"
	eventCRIURestoreStarted                = "criu_restore_started"
	eventRestoreVersionStarted             = "restore_version_started"
	eventRestoreVersionCompleted           = "restore_version_completed"
	eventRestoreDigestStarted              = "restore_digest_started"
	eventRestoreDigestCompleted            = "restore_digest_completed"
	eventRestoreCRIUStarted                = "restore_criu_started"
	eventRestoreCRIUCompleted              = "restore_criu_completed"
	eventCRIURestoreCompleted              = "criu_restore_completed"
	eventColdCapsuleStartStarted           = "cold_capsule_start_started"
	eventColdCapsuleStartCompleted         = "cold_capsule_start_completed"
	eventGenerationRestoreCompleted        = "generation_restore_completed"
	eventGenerationRestoreFailed           = "generation_restore_failed"
	eventMountsReady                       = "mounts_ready"
	eventClockChecked                      = "clock_checked"
	eventWorkerAuthorizationSent           = "worker_authorization_sent"
	eventRunnerWorkerReleased              = "runner_worker_released"
	eventRunnerWorkerGateEntered           = "runner_worker_gate_entered"
	eventRunnerWorkerGateCompleted         = "runner_worker_gate_completed"
	eventRunnerWorkerExecStarted           = "runner_worker_exec_started"
	eventRunnerWorkerExecFailed            = "runner_worker_exec_failed"
	eventJobHookValidated                  = "job_hook_validated"
	eventCustomerStepsReleased             = "customer_steps_released"
	eventJobHookReleased                   = "job_hook_released"
	eventRunnerExited                      = "runner_exited"
	eventRunnerExitObserved                = "runner_exit_observed"
	eventLeaseFailed                       = "lease_failed"
	eventCheckpointStarted                 = "checkpoint_started"
	eventQuiesceRPCStarted                 = "quiesce_rpc_started"
	eventQuiesceReceived                   = "quiesce_received"
	eventQuiesceMountsChecked              = "quiesce_mounts_checked"
	eventCheckpointDumpStarted             = "checkpoint_dump_started"
	eventCheckpointCapsulePrepareStarted   = "checkpoint_capsule_prepare_started"
	eventCheckpointCapsulePrepareCompleted = "checkpoint_capsule_prepare_completed"
	eventCheckpointVersionStarted          = "checkpoint_version_started"
	eventCheckpointVersionCompleted        = "checkpoint_version_completed"
	eventCheckpointCRIUDumpStarted         = "checkpoint_criu_dump_started"
	eventCheckpointCRIUDumpCompleted       = "checkpoint_criu_dump_completed"
	eventCheckpointDigestStarted           = "checkpoint_digest_started"
	eventCheckpointDigestCompleted         = "checkpoint_digest_completed"
	eventCheckpointDumpCompleted           = "checkpoint_dump_completed"
	eventFilesystemSyncStarted             = "filesystem_sync_started"
	eventFilesystemSyncCompleted           = "filesystem_sync_completed"
	eventQuiesceRPCCompleted               = "quiesce_rpc_completed"
	eventQuiesceRPCFailed                  = "quiesce_rpc_failed"
	eventCheckpointCompleted               = "checkpoint_completed"
	eventVMDestroyStarted                  = "vm_destroy_started"
	eventVMDestroyCompleted                = "vm_destroy_completed"
	eventSnapshotSealStarted               = "snapshot_seal_started"
	eventSnapshotSealCompleted             = "snapshot_seal_completed"
	eventIssueObserved                     = "issue_observed"
	eventClassified                        = "classified"
)

const (
	volumeWorkspace = "workspace"
	volumeProcess   = "process"
)

var allowedTraceEvents = map[string]bool{
	eventVMLaunchStarted: true, eventQEMUStarted: true,
	eventGuestHelloObserved: true, eventListenerPrepareStarted: true,
	eventListenerPrepareSent: true, eventListenerPrepareReceived: true,
	eventRunnerRegistered: true, eventPoolReady: true,
	eventRunnerAssignmentReceived: true, eventGuestAssignmentReceived: true,
	eventGuestAssignmentPublished: true,
	eventVsockAssignmentReceived:  true,
	eventHostAssignmentObserved:   true, eventAssignmentUpdateReceived: true,
	eventAssignmentObserved: true, eventGenerationMaterializationStarted: true,
	eventGenerationResolved: true, eventQMPRendezvousStarted: true,
	eventQMPConnected: true, eventWorkspaceDeviceAttached: true,
	eventProcessDeviceAttached: true, eventGuestRendezvousSent: true,
	eventRendezvousDispatched: true, eventGuestRendezvousReceived: true,
	eventMountConvergenceStarted: true, eventMountConvergenceCompleted: true,
	eventCRIURestoreStarted: true, eventCRIURestoreCompleted: true,
	eventRestoreVersionStarted: true, eventRestoreVersionCompleted: true,
	eventRestoreDigestStarted: true, eventRestoreDigestCompleted: true,
	eventRestoreCRIUStarted: true, eventRestoreCRIUCompleted: true,
	eventColdCapsuleStartStarted: true, eventColdCapsuleStartCompleted: true,
	eventGenerationRestoreCompleted: true, eventGenerationRestoreFailed: true,
	eventMountsReady:  true,
	eventClockChecked: true, eventWorkerAuthorizationSent: true,
	eventRunnerWorkerReleased: true, eventRunnerWorkerGateEntered: true,
	eventRunnerWorkerGateCompleted: true, eventRunnerWorkerExecStarted: true,
	eventRunnerWorkerExecFailed: true, eventJobHookValidated: true,
	eventCustomerStepsReleased: true, eventJobHookReleased: true,
	eventRunnerExited: true, eventRunnerExitObserved: true,
	eventLeaseFailed:       true,
	eventCheckpointStarted: true, eventQuiesceRPCStarted: true,
	eventQuiesceReceived: true, eventQuiesceMountsChecked: true,
	eventCheckpointDumpStarted:           true,
	eventCheckpointCapsulePrepareStarted: true, eventCheckpointCapsulePrepareCompleted: true,
	eventCheckpointVersionStarted: true, eventCheckpointVersionCompleted: true,
	eventCheckpointCRIUDumpStarted: true, eventCheckpointCRIUDumpCompleted: true,
	eventCheckpointDigestStarted: true, eventCheckpointDigestCompleted: true,
	eventCheckpointDumpCompleted: true,
	eventFilesystemSyncStarted:   true, eventFilesystemSyncCompleted: true,
	eventQuiesceRPCCompleted: true, eventQuiesceRPCFailed: true,
	eventCheckpointCompleted: true,
	eventVMDestroyStarted:    true, eventVMDestroyCompleted: true,
	eventSnapshotSealStarted: true, eventSnapshotSealCompleted: true,
	eventIssueObserved: true, eventClassified: true,
}

var preAssignmentEvents = map[string]bool{
	eventVMLaunchStarted: true, eventQEMUStarted: true,
	eventGuestHelloObserved: true, eventListenerPrepareStarted: true,
	eventListenerPrepareSent: true, eventListenerPrepareReceived: true,
	eventRunnerRegistered: true, eventPoolReady: true,
}

var concernCodes = map[string]bool{
	"clock_skew": true, "lifecycle_overhead": true, "platform_bug": true,
	"slower_than_blacksmith": true, "snapshot_discarded": true,
	"temporary_hardware": true,
}

var invalidCodes = map[string]bool{
	"assignment_model_not_deployed": true, "evidence_incomplete": true,
	"missing_tool": true, "preflight_failed": true,
	"workload_misconfigured": true,
}

var failCodes = map[string]bool{
	"durable_volume_unsound": true, "workload_unsupported": true,
}

type rendezvousEvent struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id,omitempty"`
	Event         string    `json:"event"`
	Seq           uint64    `json:"seq"`
	Source        string    `json:"source"`
	BootID        string    `json:"boot_id"`
	OriginSeq     uint64    `json:"origin_seq"`
	MonotonicNS   int64     `json:"monotonic_ns"`
	WallTime      time.Time `json:"wall_time"`

	Repo          string `json:"repo,omitempty"`
	Lane          string `json:"lane,omitempty"`
	JobID         int64  `json:"job_id,omitempty"`
	RunAttempt    int    `json:"run_attempt,omitempty"`
	RunnerName    string `json:"runner_name,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	RunnerJobID   string `json:"runner_job_id,omitempty"`
	VMID          string `json:"vm_id,omitempty"`
	GenerationSet string `json:"generation_set,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`

	ListenerLeaseID  string `json:"listener_lease_id,omitempty"`
	ExecutionLeaseID string `json:"execution_lease_id,omitempty"`

	Volumes        []volumeEvidence        `json:"volumes,omitempty"`
	Platform       *platformEvidence       `json:"platform,omitempty"`
	Clock          *clockEvidence          `json:"clock,omitempty"`
	Checkpoint     *checkpointEvidence     `json:"checkpoint,omitempty"`
	Issue          *issueEvidence          `json:"issue,omitempty"`
	Classification *classificationEvidence `json:"classification,omitempty"`
}

type volumeEvidence struct {
	Role            string `json:"role"`
	Dataset         string `json:"dataset"`
	Materialization string `json:"materialization"`
	SnapshotGUID    string `json:"snapshot_guid"`
	Generation      string `json:"generation"`
	DeviceSerial    string `json:"device_serial,omitempty"`
}

type platformEvidence struct {
	QEMUVersion   string `json:"qemu_version"`
	KernelRelease string `json:"kernel_release"`
	OSImageID     string `json:"os_image_id"`
	MachineType   string `json:"machine_type"`
	CPUModel      string `json:"cpu_model"`
	CRIUVersion   string `json:"criu_version"`
}

type clockEvidence struct {
	HostBeforeUnixNS  int64  `json:"host_before_unix_ns"`
	HostAfterUnixNS   int64  `json:"host_after_unix_ns"`
	GuestUnixNS       int64  `json:"guest_unix_ns"`
	MaxSkewNS         int64  `json:"max_skew_ns"`
	GuestSynchronized bool   `json:"guest_synchronized"`
	Clocksource       string `json:"clocksource"`
	AfterRestore      bool   `json:"after_restore"`
}

type checkpointEvidence struct {
	Digest  string `json:"digest"`
	Version string `json:"version"`
}

type issueEvidence struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type classificationEvidence struct {
	Outcome                       benchmarkOutcome `json:"outcome"`
	Code                          string           `json:"code"`
	Detail                        string           `json:"detail"`
	TraditionalContainerAttempted bool             `json:"traditional_container_attempted,omitempty"`
	DurableToolchainAttempted     bool             `json:"durable_toolchain_attempted,omitempty"`
}

type rendezvousTraceReport struct {
	SchemaVersion    int              `json:"schema_version"`
	RunID            string           `json:"run_id"`
	Repo             string           `json:"repo,omitempty"`
	Lane             string           `json:"lane,omitempty"`
	JobID            int64            `json:"job_id,omitempty"`
	RunAttempt       int              `json:"run_attempt,omitempty"`
	RunnerName       string           `json:"runner_name,omitempty"`
	VMID             string           `json:"vm_id,omitempty"`
	GenerationSet    string           `json:"generation_set,omitempty"`
	ListenerLeaseID  string           `json:"listener_lease_id,omitempty"`
	ExecutionLeaseID string           `json:"execution_lease_id,omitempty"`
	RestoreMode      string           `json:"restore_mode,omitempty"`
	Events           int              `json:"events"`
	DurationsNS      map[string]int64 `json:"durations_ns,omitempty"`
	ClockSkewBoundNS int64            `json:"clock_skew_bound_ns,omitempty"`
	Outcome          benchmarkOutcome `json:"outcome"`
	TraceValid       bool             `json:"trace_valid"`
	Violations       []string         `json:"violations,omitempty"`
	Concerns         []string         `json:"concerns,omitempty"`
}

func readRendezvousTrace(r io.Reader) ([]rendezvousEvent, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var events []rendezvousEvent
	for line := 1; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var event rendezvousEvent
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("trace line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("trace line %d contains more than one JSON value", line)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("trace contains no events")
	}
	return events, nil
}

func validateRendezvousTrace(events []rendezvousEvent) *rendezvousTraceReport {
	return validateRendezvousTraceScope(events, false)
}

func validateRendezvousTraceScope(events []rendezvousEvent, throughRelease bool) *rendezvousTraceReport {
	report := &rendezvousTraceReport{
		SchemaVersion: rendezvousTraceSchema, Events: len(events),
		DurationsNS: map[string]int64{}, Outcome: outcomePass, TraceValid: true,
	}
	seen := map[string]*rendezvousEvent{}
	lastMonotonic := map[string]int64{}
	lastOriginSeq := map[string]uint64{}
	var resolvedVolumes, boundVolumes []volumeEvidence
	var platform *platformEvidence
	var checkpoint *checkpointEvidence
	var explicit *classificationEvidence

	violate := func(format string, args ...any) {
		report.Violations = append(report.Violations, fmt.Sprintf(format, args...))
	}
	concern := func(format string, args ...any) {
		report.Concerns = append(report.Concerns, fmt.Sprintf(format, args...))
	}

	var previousSeq uint64
	for index := range events {
		event := &events[index]
		if event.SchemaVersion != rendezvousTraceSchema {
			violate("event %d uses schema_version %d, want %d", index+1, event.SchemaVersion, rendezvousTraceSchema)
		}
		if !allowedTraceEvents[event.Event] {
			violate("event %d has unknown event %q", index+1, event.Event)
			continue
		}
		if event.Seq == 0 || (index > 0 && event.Seq <= previousSeq) {
			violate("event %s has seq %d after %d; collector order must be strictly increasing", event.Event, event.Seq, previousSeq)
		}
		previousSeq = event.Seq
		if event.Source == "" || event.BootID == "" || event.OriginSeq == 0 || event.MonotonicNS <= 0 {
			violate("event %s at seq %d lacks a complete source/boot/origin/monotonic clock tuple", event.Event, event.Seq)
		} else {
			domain := event.Source + "\x00" + event.BootID
			if lastOriginSeq[domain] >= event.OriginSeq {
				violate("clock domain %s/%s origin_seq moved from %d to %d", event.Source, event.BootID, lastOriginSeq[domain], event.OriginSeq)
			}
			if lastMonotonic[domain] > event.MonotonicNS {
				violate("clock domain %s/%s monotonic_ns moved backward from %d to %d", event.Source, event.BootID, lastMonotonic[domain], event.MonotonicNS)
			}
			lastOriginSeq[domain], lastMonotonic[domain] = event.OriginSeq, event.MonotonicNS
		}
		if event.WallTime.IsZero() {
			violate("event %s at seq %d has no wall_time", event.Event, event.Seq)
		}
		if seen[event.Event] != nil && event.Event != eventIssueObserved {
			violate("event %s appears more than once", event.Event)
		} else if event.Event != eventIssueObserved {
			seen[event.Event] = event
		}
		if preAssignmentEvents[event.Event] {
			validateUnownedBootstrap(*event, violate)
		}
		mergeTraceIdentity(report, *event, preAssignmentEvents[event.Event], violate)

		switch event.Event {
		case eventPoolReady:
			if event.RunnerName == "" || event.VMID == "" || event.ListenerLeaseID == "" {
				violate("pool_ready requires runner_name, vm_id, and listener_lease_id")
			}
			if event.RunnerName != event.ListenerLeaseID {
				violate("pool_ready runner_name %q does not match listener_lease_id %q", event.RunnerName, event.ListenerLeaseID)
			}
			validatePlatform(event.Platform, violate)
			platform = event.Platform
		case eventAssignmentUpdateReceived, eventAssignmentObserved:
			validateExactAssignment(*event, violate)
		case eventGenerationResolved:
			if event.Repo == "" || event.GenerationSet == "" {
				violate("generation_resolved requires repo and generation_set")
			}
			validateVolumes(event.Volumes, platform, false, event.ExecutionLeaseID, violate)
			resolvedVolumes = append([]volumeEvidence(nil), event.Volumes...)
		case eventRendezvousDispatched:
			validateExactAssignment(*event, violate)
			validateVolumes(event.Volumes, platform, true, event.ExecutionLeaseID, violate)
			compareVolumes(event.Event, resolvedVolumes, event.Volumes, violate)
			boundVolumes = append([]volumeEvidence(nil), event.Volumes...)
		case eventMountsReady:
			validateVolumes(event.Volumes, platform, true, event.ExecutionLeaseID, violate)
			compareVolumes(event.Event, boundVolumes, event.Volumes, violate)
		case eventCheckpointCompleted, eventSnapshotSealStarted, eventSnapshotSealCompleted:
			validateCheckpoint(event.Event, event.Checkpoint, violate)
			if checkpoint == nil {
				checkpoint = event.Checkpoint
			} else if event.Checkpoint != nil && *checkpoint != *event.Checkpoint {
				violate("%s checkpoint artifact differs from checkpoint_completed", event.Event)
			}
		case eventIssueObserved:
			if event.Issue == nil || event.Issue.Code == "" || event.Issue.Detail == "" {
				violate("issue_observed requires issue.code and issue.detail")
			} else if !concernCodes[event.Issue.Code] {
				violate("issue_observed code %q is not a benchmark concern code", event.Issue.Code)
			} else {
				concern("%s: %s", event.Issue.Code, event.Issue.Detail)
			}
		case eventClassified:
			if event.Classification == nil {
				violate("classified requires classification")
			} else {
				validateClassification(event.Classification, violate)
				explicit = event.Classification
			}
		}
	}

	if len(boundVolumes) == 2 {
		if boundVolumes[0].Materialization == "clone" {
			report.RestoreMode = "warm"
		} else if boundVolumes[0].Materialization == "empty" {
			report.RestoreMode = "cold"
		}
	}
	if clock := seen[eventClockChecked]; clock != nil {
		report.ClockSkewBoundNS = validateClock(clock.Clock, report.RestoreMode == "warm", concern, violate)
	}

	if explicit == nil || explicit.Outcome == outcomePass || explicit.Outcome == outcomeConcern {
		required := []string{
			eventGuestHelloObserved, eventListenerPrepareStarted, eventListenerPrepareSent,
			eventListenerPrepareReceived, eventRunnerRegistered, eventPoolReady,
			eventRunnerAssignmentReceived, eventGuestAssignmentReceived,
			eventHostAssignmentObserved, eventAssignmentUpdateReceived,
			eventAssignmentObserved, eventGenerationMaterializationStarted,
			eventGenerationResolved, eventQMPRendezvousStarted,
			eventQMPConnected, eventWorkspaceDeviceAttached,
			eventProcessDeviceAttached, eventGuestRendezvousSent,
			eventRendezvousDispatched, eventGuestRendezvousReceived,
			eventMountConvergenceStarted, eventMountConvergenceCompleted,
			eventGenerationRestoreCompleted, eventMountsReady, eventClockChecked,
			eventWorkerAuthorizationSent, eventRunnerWorkerReleased,
			eventRunnerWorkerExecStarted,
			eventJobHookValidated, eventCustomerStepsReleased, eventJobHookReleased,
		}
		switch report.RestoreMode {
		case "cold":
			required = append(required, eventColdCapsuleStartStarted, eventColdCapsuleStartCompleted)
			for _, forbidden := range []string{eventCRIURestoreStarted, eventCRIURestoreCompleted} {
				if seen[forbidden] != nil {
					violate("cold rendezvous unexpectedly contains %s", forbidden)
				}
			}
		case "warm":
			required = append(required, eventCRIURestoreStarted, eventCRIURestoreCompleted)
			for _, forbidden := range []string{eventColdCapsuleStartStarted, eventColdCapsuleStartCompleted} {
				if seen[forbidden] != nil {
					violate("warm rendezvous unexpectedly contains %s", forbidden)
				}
			}
		default:
			violate("rendezvous does not prove a paired cold or warm generation")
		}
		if !throughRelease {
			required = append(required,
				eventRunnerExited, eventRunnerExitObserved, eventCheckpointStarted,
				eventQuiesceRPCStarted, eventQuiesceReceived,
				eventQuiesceMountsChecked, eventCheckpointDumpStarted,
				eventCheckpointDumpCompleted, eventFilesystemSyncStarted,
				eventFilesystemSyncCompleted, eventQuiesceRPCCompleted,
				eventCheckpointCompleted, eventVMDestroyStarted,
				eventVMDestroyCompleted, eventSnapshotSealStarted,
				eventSnapshotSealCompleted,
			)
		}
		for _, name := range required {
			if seen[name] == nil {
				violate("complete trace is missing %s", name)
			}
		}
		validateCollectorOrder(seen, throughRelease, violate)
	}
	deriveDurations(report.DurationsNS, seen)
	if len(report.DurationsNS) == 0 {
		report.DurationsNS = nil
	}

	if len(report.Violations) > 0 {
		report.TraceValid = false
		report.Outcome = outcomeInvalid
		return report
	}
	if explicit != nil {
		report.Outcome = explicit.Outcome
	}
	if report.Outcome == outcomePass && len(report.Concerns) > 0 {
		report.Outcome = outcomeConcern
	}
	return report
}

func validateUnownedBootstrap(event rendezvousEvent, violate func(string, ...any)) {
	if event.RunID != "" || event.Repo != "" || event.JobID != 0 || event.RunAttempt != 0 ||
		event.RequestID != "" || event.RunnerJobID != "" || event.ExecutionLeaseID != "" ||
		event.GenerationSet != "" || len(event.Volumes) != 0 {
		violate("pre-assignment event %s carries customer identity or volumes", event.Event)
	}
}

func validateExactAssignment(event rendezvousEvent, violate func(string, ...any)) {
	if event.RunID == "" || event.JobID <= 0 || event.RunAttempt <= 0 || event.RunnerName == "" ||
		event.RequestID == "" || event.RunnerJobID == "" || event.ListenerLeaseID == "" ||
		event.ExecutionLeaseID == "" || event.VMID == "" {
		violate("%s requires exact provider, listener, execution, request, runner-job, and VM identity", event.Event)
	}
	if event.RunnerName != event.ListenerLeaseID {
		violate("%s runner_name %q does not match listener_lease_id %q", event.Event, event.RunnerName, event.ListenerLeaseID)
	}
}

func mergeTraceIdentity(report *rendezvousTraceReport, event rendezvousEvent, bootstrap bool, violate func(string, ...any)) {
	for _, field := range []struct {
		name string
		got  string
		dst  *string
	}{
		{"lane", event.Lane, &report.Lane},
		{"runner_name", event.RunnerName, &report.RunnerName},
		{"vm_id", event.VMID, &report.VMID},
		{"listener_lease_id", event.ListenerLeaseID, &report.ListenerLeaseID},
	} {
		if field.got == "" {
			continue
		}
		if *field.dst == "" {
			*field.dst = field.got
		} else if *field.dst != field.got {
			violate("event %s changes %s from %q to %q", event.Event, field.name, *field.dst, field.got)
		}
	}
	if bootstrap {
		return
	}
	for _, field := range []struct {
		name string
		got  string
		dst  *string
	}{
		{"run_id", event.RunID, &report.RunID}, {"repo", event.Repo, &report.Repo},
		{"execution_lease_id", event.ExecutionLeaseID, &report.ExecutionLeaseID},
	} {
		if field.got == "" {
			continue
		}
		if *field.dst == "" {
			*field.dst = field.got
		} else if *field.dst != field.got {
			violate("event %s changes %s from %q to %q", event.Event, field.name, *field.dst, field.got)
		}
	}
	if event.GenerationSet != "" && event.Event != eventSnapshotSealCompleted {
		if report.GenerationSet == "" {
			report.GenerationSet = event.GenerationSet
		} else if report.GenerationSet != event.GenerationSet {
			violate("event %s changes generation_set from %q to %q", event.Event, report.GenerationSet, event.GenerationSet)
		}
	}
	if event.JobID > 0 {
		if report.JobID == 0 {
			report.JobID = event.JobID
		} else if report.JobID != event.JobID {
			violate("event %s changes job_id from %d to %d", event.Event, report.JobID, event.JobID)
		}
	}
	if event.RunAttempt > 0 {
		if report.RunAttempt == 0 {
			report.RunAttempt = event.RunAttempt
		} else if report.RunAttempt != event.RunAttempt {
			violate("event %s changes run_attempt from %d to %d", event.Event, report.RunAttempt, event.RunAttempt)
		}
	}
}

func validatePlatform(platform *platformEvidence, violate func(string, ...any)) {
	if platform == nil {
		violate("pool_ready requires a platform fingerprint")
		return
	}
	for _, field := range []struct{ name, value string }{
		{"qemu_version", platform.QEMUVersion}, {"kernel_release", platform.KernelRelease},
		{"os_image_id", platform.OSImageID}, {"machine_type", platform.MachineType},
		{"cpu_model", platform.CPUModel}, {"criu_version", platform.CRIUVersion},
	} {
		if field.value == "" {
			violate("platform fingerprint has no %s", field.name)
		}
	}
}

func validateVolumes(volumes []volumeEvidence, platform *platformEvidence, requireSerial bool, executionLeaseID string, violate func(string, ...any)) {
	byRole := map[string]volumeEvidence{}
	datasets := map[string]bool{}
	serials := map[string]bool{}
	for _, volume := range volumes {
		if volume.Role != volumeWorkspace && volume.Role != volumeProcess {
			violate("rendezvous contains unknown volume role %q", volume.Role)
		}
		if _, ok := byRole[volume.Role]; ok {
			violate("rendezvous contains duplicate %s volume", volume.Role)
		}
		byRole[volume.Role] = volume
		if volume.Dataset == "" {
			violate("rendezvous %s volume lacks dataset", volume.Role)
		} else if datasets[volume.Dataset] {
			violate("rendezvous contains duplicate dataset %q", volume.Dataset)
		}
		datasets[volume.Dataset] = true
		if executionLeaseID != "" && volume.Dataset != "" {
			lease := volume.Dataset
			if separator := strings.LastIndexByte(lease, '/'); separator >= 0 {
				lease = lease[separator+1:]
			}
			if lease != executionLeaseID {
				violate("%s dataset %q belongs to execution lease %q, want %q", volume.Role, volume.Dataset, lease, executionLeaseID)
			}
		}
		switch volume.Materialization {
		case "empty":
			if volume.Generation != "" || volume.SnapshotGUID != "" {
				violate("empty rendezvous %s volume names a source generation or snapshot_guid", volume.Role)
			}
		case "clone":
			if volume.Generation == "" || volume.SnapshotGUID == "" {
				violate("cloned rendezvous %s volume lacks generation or snapshot_guid", volume.Role)
			}
		case "":
			violate("rendezvous %s volume lacks materialization", volume.Role)
		default:
			violate("rendezvous %s volume has unknown materialization %q", volume.Role, volume.Materialization)
		}
		if requireSerial && volume.DeviceSerial == "" {
			violate("rendezvous %s volume lacks device_serial", volume.Role)
		}
		if volume.DeviceSerial != "" {
			if serials[volume.DeviceSerial] {
				violate("rendezvous contains duplicate device_serial %q", volume.DeviceSerial)
			}
			serials[volume.DeviceSerial] = true
		}
	}
	workspace, haveWorkspace := byRole[volumeWorkspace]
	process, haveProcess := byRole[volumeProcess]
	if !haveWorkspace || !haveProcess || len(volumes) != 2 {
		violate("rendezvous must contain exactly one workspace and one process volume")
		return
	}
	if workspace.Materialization != process.Materialization {
		violate("workspace and process volumes have different materialization modes")
	}
	if workspace.Materialization == "clone" && workspace.Generation != process.Generation {
		violate("workspace and process volumes do not share one generation")
	}
	if workspace.Materialization == "clone" && (platform == nil || platform.CRIUVersion == "") {
		violate("warm rendezvous has no CRIU version in the platform fingerprint")
	}
}

func compareVolumes(event string, expected, observed []volumeEvidence, violate func(string, ...any)) {
	expectedByRole := make(map[string]volumeEvidence, len(expected))
	for _, volume := range expected {
		expectedByRole[volume.Role] = volume
	}
	if len(observed) != len(expected) {
		violate("%s observed %d volumes, want %d", event, len(observed), len(expected))
	}
	for _, volume := range observed {
		want, ok := expectedByRole[volume.Role]
		if !ok {
			violate("%s observed unexpected %s volume", event, volume.Role)
			continue
		}
		if volume.Dataset != want.Dataset || volume.Materialization != want.Materialization ||
			volume.SnapshotGUID != want.SnapshotGUID || volume.Generation != want.Generation {
			violate("%s %s volume does not match the resolved generation tuple", event, volume.Role)
		}
		if want.DeviceSerial != "" && volume.DeviceSerial != want.DeviceSerial {
			violate("%s %s volume device_serial %q does not match %q", event, volume.Role, volume.DeviceSerial, want.DeviceSerial)
		}
	}
}

func validateClock(clock *clockEvidence, afterRestore bool, concern, violate func(string, ...any)) int64 {
	if clock == nil {
		violate("clock_checked requires clock evidence")
		return 0
	}
	if clock.HostBeforeUnixNS <= 0 || clock.HostAfterUnixNS <= 0 || clock.GuestUnixNS <= 0 {
		violate("clock evidence requires positive host and guest realtime samples")
		return 0
	}
	if clock.HostAfterUnixNS < clock.HostBeforeUnixNS {
		violate("clock host bracket moved backward")
		return 0
	}
	if clock.MaxSkewNS <= 0 || clock.Clocksource == "" {
		violate("clock evidence requires max_skew_ns and guest clocksource")
	}
	if clock.AfterRestore != afterRestore {
		violate("clock after_restore=%t does not match %s rendezvous", clock.AfterRestore, map[bool]string{true: "warm", false: "cold"}[afterRestore])
	}
	midpoint := clock.HostBeforeUnixNS + (clock.HostAfterUnixNS-clock.HostBeforeUnixNS)/2
	uncertainty := (clock.HostAfterUnixNS - clock.HostBeforeUnixNS) / 2
	bound := abs64(clock.GuestUnixNS-midpoint) + uncertainty
	if clock.MaxSkewNS > 0 && bound > clock.MaxSkewNS {
		concern("clock_skew: conservative offset bound %s exceeds %s", time.Duration(bound), time.Duration(clock.MaxSkewNS))
	}
	if !clock.GuestSynchronized {
		concern("clock_skew: guest time synchronization was not healthy")
	}
	return bound
}

func validateCheckpoint(event string, checkpoint *checkpointEvidence, violate func(string, ...any)) {
	if checkpoint == nil || checkpoint.Digest == "" || checkpoint.Version == "" {
		violate("%s requires checkpoint digest and version", event)
	}
}

func validateCollectorOrder(seen map[string]*rendezvousEvent, throughRelease bool, violate func(string, ...any)) {
	chains := [][]string{{
		eventPoolReady, eventAssignmentUpdateReceived, eventAssignmentObserved,
		eventGenerationMaterializationStarted, eventGenerationResolved,
		eventRendezvousDispatched, eventMountsReady, eventClockChecked,
		eventWorkerAuthorizationSent, eventRunnerWorkerExecStarted,
		eventJobHookReleased,
	}}
	if !throughRelease {
		chains = append(chains, []string{
			eventRunnerExitObserved, eventCheckpointStarted, eventCheckpointCompleted,
			eventVMDestroyStarted, eventVMDestroyCompleted,
			eventSnapshotSealStarted, eventSnapshotSealCompleted,
		})
	}
	for _, chain := range chains {
		var previous *rendezvousEvent
		for _, name := range chain {
			current := seen[name]
			if current == nil {
				continue
			}
			if previous != nil && current.Seq <= previous.Seq {
				violate("collector observed %s at seq %d before %s at seq %d", current.Event, current.Seq, previous.Event, previous.Seq)
			}
			previous = current
		}
	}
}

func deriveDurations(out map[string]int64, seen map[string]*rendezvousEvent) {
	for name, pair := range map[string][2]string{
		"qemu_process_start":                 {eventVMLaunchStarted, eventQEMUStarted},
		"assignment_observation":             {eventAssignmentUpdateReceived, eventAssignmentObserved},
		"generation_materialization":         {eventGenerationMaterializationStarted, eventGenerationResolved},
		"assignment_to_rendezvous_dispatch":  {eventAssignmentObserved, eventRendezvousDispatched},
		"qmp_rendezvous":                     {eventQMPRendezvousStarted, eventGuestRendezvousSent},
		"guest_mount_convergence":            {eventMountConvergenceStarted, eventMountConvergenceCompleted},
		"criu_restore":                       {eventCRIURestoreStarted, eventCRIURestoreCompleted},
		"restore_version_validation":         {eventRestoreVersionStarted, eventRestoreVersionCompleted},
		"restore_digest_validation":          {eventRestoreDigestStarted, eventRestoreDigestCompleted},
		"restore_criu":                       {eventRestoreCRIUStarted, eventRestoreCRIUCompleted},
		"cold_capsule_start":                 {eventColdCapsuleStartStarted, eventColdCapsuleStartCompleted},
		"assignment_publication":             {eventGuestAssignmentReceived, eventGuestAssignmentPublished},
		"assignment_to_worker_authorization": {eventAssignmentObserved, eventWorkerAuthorizationSent},
		"worker_gate":                        {eventRunnerWorkerGateEntered, eventRunnerWorkerGateCompleted},
		"worker_exec_dispatch":               {eventRunnerWorkerReleased, eventRunnerWorkerExecStarted},
		"worker_to_job_hook":                 {eventRunnerWorkerExecStarted, eventJobHookValidated},
		"job_hook_gate":                      {eventJobHookValidated, eventCustomerStepsReleased},
		"checkpoint_dump":                    {eventCheckpointDumpStarted, eventCheckpointDumpCompleted},
		"checkpoint_capsule_prepare":         {eventCheckpointCapsulePrepareStarted, eventCheckpointCapsulePrepareCompleted},
		"checkpoint_version":                 {eventCheckpointVersionStarted, eventCheckpointVersionCompleted},
		"checkpoint_criu_dump":               {eventCheckpointCRIUDumpStarted, eventCheckpointCRIUDumpCompleted},
		"checkpoint_digest":                  {eventCheckpointDigestStarted, eventCheckpointDigestCompleted},
		"filesystem_sync":                    {eventFilesystemSyncStarted, eventFilesystemSyncCompleted},
		"quiesce_rpc":                        {eventQuiesceRPCStarted, eventQuiesceRPCCompleted},
		"checkpoint_lifecycle":               {eventCheckpointStarted, eventCheckpointCompleted},
		"vm_destroy":                         {eventVMDestroyStarted, eventVMDestroyCompleted},
		"snapshot_seal":                      {eventSnapshotSealStarted, eventSnapshotSealCompleted},
	} {
		start, end := seen[pair[0]], seen[pair[1]]
		if start == nil || end == nil || start.Source != end.Source || start.BootID != end.BootID || end.MonotonicNS < start.MonotonicNS {
			continue
		}
		out[name] = end.MonotonicNS - start.MonotonicNS
	}
	for name, pair := range map[string][2]string{
		"vsock_to_assignment_update":   {eventVsockAssignmentReceived, eventAssignmentUpdateReceived},
		"vsock_to_assignment_observed": {eventVsockAssignmentReceived, eventAssignmentObserved},
	} {
		start, end := seen[pair[0]], seen[pair[1]]
		if start == nil || end == nil || start.BootID != end.BootID || end.MonotonicNS < start.MonotonicNS ||
			!strings.HasPrefix(start.Source, "hostd-") || !strings.HasPrefix(end.Source, "hostd-") {
			continue
		}
		out[name] = end.MonotonicNS - start.MonotonicNS
	}
}

func validateClassification(classification *classificationEvidence, violate func(string, ...any)) {
	if classification.Code == "" || classification.Detail == "" {
		violate("classification requires code and detail")
		return
	}
	switch classification.Outcome {
	case outcomePass:
		if classification.Code != "completed" {
			violate("PASS classification code %q must be completed", classification.Code)
		}
	case outcomeConcern:
		if !concernCodes[classification.Code] {
			violate("CONCERN classification code %q is not allowed", classification.Code)
		}
	case outcomeInvalid:
		if !invalidCodes[classification.Code] {
			violate("INVALID classification code %q is not allowed", classification.Code)
		}
	case outcomeFail:
		if !failCodes[classification.Code] {
			violate("FAIL classification code %q is not allowed", classification.Code)
		}
		if classification.Code == "workload_unsupported" &&
			(!classification.TraditionalContainerAttempted || !classification.DurableToolchainAttempted) {
			violate("workload_unsupported requires both container and durable-toolchain paths to have been attempted")
		}
	default:
		violate("unknown classification outcome %q", classification.Outcome)
	}
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func printRendezvousTraceReport(w io.Writer, report *rendezvousTraceReport) {
	fmt.Fprintf(w, "postflight rendezvous trace — run %s\n", report.RunID)
	fmt.Fprintf(w, "job=%d attempt=%d runner=%s listener=%s execution=%s vm=%s generation_set=%s restore=%s events=%d\n",
		report.JobID, report.RunAttempt, report.RunnerName, report.ListenerLeaseID,
		report.ExecutionLeaseID, report.VMID, report.GenerationSet, report.RestoreMode, report.Events)
	fmt.Fprintf(w, "outcome: %s (trace_valid=%t)\n", report.Outcome, report.TraceValid)
	for _, violation := range report.Violations {
		fmt.Fprintf(w, "INVALID: %s\n", violation)
	}
	for _, item := range report.Concerns {
		fmt.Fprintf(w, "CONCERN: %s\n", item)
	}
}

func cmdValidateRendezvous(args []string) error {
	var tracePath, jsonPath string
	var throughRelease bool
	fs := flag.NewFlagSet("validate-rendezvous", flag.ContinueOnError)
	fs.StringVar(&tracePath, "trace", "", "JSONL rendezvous trace to validate (required)")
	fs.StringVar(&jsonPath, "json", "", "JSON report output path (default <trace>.report.json)")
	fs.BoolVar(&throughRelease, "through-release", false, "validate exact assignment, paired rendezvous, restore, and customer-step release without requiring post-job checkpoint/seal evidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tracePath == "" {
		return fmt.Errorf("-trace is required")
	}
	if jsonPath == "" {
		jsonPath = tracePath + ".report.json"
	}
	file, err := os.Open(tracePath)
	if err != nil {
		return err
	}
	defer file.Close()
	events, err := readRendezvousTrace(file)
	if err != nil {
		return err
	}
	report := validateRendezvousTraceScope(events, throughRelease)
	printRendezvousTraceReport(os.Stdout, report)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		return err
	}
	if report.Outcome == outcomeInvalid || report.Outcome == outcomeFail {
		return fmt.Errorf("rendezvous trace outcome %s", strings.ToLower(string(report.Outcome)))
	}
	return nil
}
