package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func (a *Agent) Tick(ctx context.Context) {
	a.mu.Lock()
	if !a.synced {
		a.mu.Unlock()
		return
	}
	members := cloneMap(a.desiredMembers)
	assignments := cloneMap(a.assignments)
	desiredAssignments := cloneMap(a.desiredAssignments)
	quarantinedMembers := cloneMap(a.quarantinedMembers)
	quarantinedJobs := cloneMap(a.quarantinedJobs)
	reap := append([]zvol.GenerationID(nil), a.reap...)
	poolTargets := cloneMap(a.poolTargets)
	a.mu.Unlock()
	started := time.Now()
	view, err := a.listVMs(ctx)
	if err != nil {
		a.logger.Error("listing vms", "err", err)
		return
	}
	admission := a.storageAdmission(ctx)
	a.recycleUnownedUnusableVMs(ctx, view, assignments)
	a.stepMembers(ctx, view, members, quarantinedMembers, assignments, admission.Admitted)
	for _, id := range sortedAssignmentIDs(assignments) {
		record := assignments[id]
		record.mu.Lock()
		if !quarantinedJobs[id] {
			a.stepAssignment(ctx, record, view)
		}
		record.mu.Unlock()
	}
	a.reapGenerations(ctx, desiredAssignments, reap)
	if !admission.Admitted {
		for class := range poolTargets {
			poolTargets[class] = 0
		}
	}
	a.reconcilePool(ctx, view, poolTargets, assignments)
	a.collectOrphans(ctx, view, assignments, desiredAssignments, quarantinedJobs)
	a.mu.Lock()
	traceMembers := cloneMap(a.desiredMembers)
	traceAssignments := cloneMap(a.assignments)
	a.mu.Unlock()
	a.pruneTraces(traceMembers, traceAssignments)
	a.logger.Info("postflight.hostd.convergence.completed",
		"duration_ns", time.Since(started).Nanoseconds(), "vms", len(view.byID),
		"members", len(members), "assignments", len(assignments))
}

func cloneMap[K comparable, V any](input map[K]V) map[K]V {
	output := make(map[K]V, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

type vmView struct {
	byID      map[vm.ID]vm.Status
	byMember  map[string]vm.Status
	warm      map[vm.Class][]vm.ID
	countByCl map[vm.Class]int
}

func (a *Agent) listVMs(ctx context.Context) (*vmView, error) {
	statuses, err := a.vms.List(ctx)
	if err != nil {
		return nil, err
	}
	view := &vmView{
		byID: map[vm.ID]vm.Status{}, byMember: map[string]vm.Status{},
		warm: map[vm.Class][]vm.ID{}, countByCl: map[vm.Class]int{},
	}
	for _, status := range statuses {
		view.byID[status.ID] = status
		view.countByCl[status.Class]++
		if status.Incarnation != "" {
			view.byMember[status.Incarnation] = status
		}
		if status.Phase == vm.PhaseWarm {
			desiredImage := a.cfg.Images[status.Class]
			if desiredImage == "" || status.Image == desiredImage {
				view.warm[status.Class] = append(view.warm[status.Class], status.ID)
			}
		}
	}
	for _, ids := range view.warm {
		sortVMIDs(ids)
	}
	return view, nil
}

func (a *Agent) stepMembers(ctx context.Context, view *vmView, members map[string]syncproto.DesiredPoolMember, quarantined map[string]bool, assignments map[string]*assignment, storageAdmitted bool) {
	for memberID, desired := range members {
		if quarantined[memberID] {
			continue
		}
		status, ok := view.byMember[memberID]
		if !ok {
			continue
		}
		trace, err := a.traceFor(memberID, desired.RunnerName, string(status.ID))
		if err != nil {
			a.logger.Error("opening pool-member evidence", "member_id", memberID, "vm", status.ID, "err", err)
		} else {
			a.appendBootstrapTiming(trace, status.Timing)
			if trace != nil && (a.traceEventSeen(trace, "runner_registered") || status.Phase == vm.PhaseListening) {
				a.appendTrace(trace, nil, "pool_ready", func(event *traceEvent) {
					event.Platform = a.platformEvidence()
				})
			}
		}
		assignmentOwned := assignmentOwnsMember(assignments, memberID)
		if desired.State == syncproto.DesiredMemberRecycle {
			if assignmentOwned {
				continue
			}
			if err := a.vms.Destroy(ctx, status.ID); err != nil {
				a.logger.Error("recycling pool member", "member_id", memberID, "vm", status.ID, "err", err)
			}
			continue
		}
		if status.Phase == vm.PhaseRecycleRequired || status.Phase == vm.PhaseExited {
			continue
		}
		if status.Phase != vm.PhaseWarm {
			continue
		}
		if desired.JITConfig == "" {
			continue
		}
		if !storageAdmitted {
			continue
		}
		started := time.Now()
		if err := a.vms.Prepare(ctx, status.ID, vm.Preparation{
			MemberID: memberID, JITConfig: desired.JITConfig,
			Env: map[string]string{
				"ACTIONS_RUNNER_HOOK_JOB_STARTED":       "/usr/local/libexec/postflight-job-started.sh",
				"GITHUB_ACTIONS_RUNNER_CHANNEL_TIMEOUT": "300",
			},
		}); err != nil {
			a.logger.Error("preparing pool member", "member_id", memberID, "vm", status.ID, "err", err)
			continue
		}
		a.logger.Info("postflight.hostd.listener.prepare_sent", "member_id", memberID, "vm", status.ID, "duration_ns", time.Since(started).Nanoseconds())
	}
}

func assignmentOwnsMember(assignments map[string]*assignment, memberID string) bool {
	for _, record := range assignments {
		if record.memberID == memberID {
			return true
		}
	}
	return false
}

func (a *Agent) stepStatus(ctx context.Context, status vm.Status, assignments map[string]*assignment, quarantined map[string]bool) {
	for id, record := range assignments {
		record.mu.Lock()
		if record.spec.MemberID == status.Incarnation {
			if !quarantined[id] {
				a.stepAssignment(ctx, record, &vmView{
					byID:     map[vm.ID]vm.Status{status.ID: status},
					byMember: map[string]vm.Status{status.Incarnation: status},
					warm:     map[vm.Class][]vm.ID{}, countByCl: map[vm.Class]int{status.Class: 1},
				})
			}
			record.mu.Unlock()
			return
		}
		record.mu.Unlock()
	}
}

func (a *Agent) stepAssignment(ctx context.Context, record *assignment, view *vmView) {
	if record.termination != "" {
		a.finishPendingTermination(ctx, record)
		return
	}
	if record.state.Terminal() {
		if record.vmID != "" {
			a.destroyAssignmentVM(ctx, record)
		}
		return
	}
	// The VM is deliberately gone after a successful runner exit. Generation
	// finalization is control-plane driven and must not reinterpret that
	// expected absence as a pre-job crash.
	if record.state == syncproto.AssignmentExited {
		a.finalizeExitedAssignment(ctx, record)
		return
	}
	status, exists := view.byMember[record.spec.MemberID]
	if !exists || status.Phase == vm.PhaseGone {
		if !record.state.Terminal() {
			record.vmID = ""
			a.failClosed(ctx, record, "pool member disappeared after provider acquisition")
		}
		return
	}
	record.vmID = string(status.ID)
	if record.trace == nil {
		trace, err := a.traceFor(record.spec.MemberID, record.spec.Identity.RunnerName, record.vmID)
		if err != nil {
			a.logger.Error("opening assignment evidence", "assignment_id", record.spec.AssignmentID, "member_id", record.spec.MemberID, "err", err)
		} else {
			record.trace = trace
		}
	}
	if record.updateTiming.Event != "" {
		a.appendOriginTiming(record.trace, record, []vm.TimingPoint{record.updateTiming})
	}
	a.appendOriginTiming(record.trace, record, status.Timing)
	record.timing = mergeTiming(record.timing, status.Timing)
	if deadline, ok := assignmentDeadlines[record.state]; ok && a.now().Sub(record.since) > deadline {
		a.failClosed(ctx, record, "deadline exceeded in "+string(record.state))
		return
	}
	if record.spec.State == syncproto.DesiredAssignmentAbort {
		a.recycleAndComplete(ctx, record, "assignment withdrawn by provider")
		return
	}
	if status.Phase == vm.PhaseRecycleRequired {
		a.captureRestore(record, status)
		a.failClosed(ctx, record, "guest requested recycle: "+status.FailureReason)
		return
	}
	if status.Phase == vm.PhaseExited && record.state != syncproto.AssignmentExited {
		a.finishAssignment(ctx, record, status)
		return
	}

	switch record.state {
	case syncproto.AssignmentObserved:
		if status.Phase != vm.PhaseJobAssigned {
			return
		}
		if err := validateBinding(record.spec, status); err != nil {
			a.failClosed(ctx, record, "binding identity: "+err.Error())
			return
		}
		a.appendTrace(record.trace, record, "assignment_observed", nil)
		admission := a.storageAdmission(ctx)
		if !admission.Admitted {
			a.metrics.StorageAdmissionFailures.Add(1)
			a.appendTrace(record.trace, record, "storage_admission_rejected", func(event *traceEvent) {
				event.FailureReason = admission.Reason
			})
			a.failClosed(ctx, record, admission.Reason)
			return
		}
		started := time.Now()
		a.appendTrace(record.trace, record, "generation_materialization_started", nil)
		if err := a.materialize(ctx, record); err != nil {
			a.failClosed(ctx, record, "materialize generation: "+err.Error())
			return
		}
		a.appendTrace(record.trace, record, "generation_resolved", func(event *traceEvent) {
			event.GenerationSet = generationSet(record)
			event.Volumes = traceVolumes(record, false)
		})
		record.hostBeforeUnixNS = time.Now().UnixNano()
		if err := a.vms.Rendezvous(ctx, status.ID, a.rendezvous(record)); err != nil {
			a.failClosed(ctx, record, "rendezvous: "+err.Error())
			return
		}
		a.appendTrace(record.trace, record, "rendezvous_dispatched", func(event *traceEvent) {
			event.GenerationSet = generationSet(record)
			event.Volumes = traceVolumes(record, true)
		})
		record.enter(syncproto.AssignmentBinding, a.now())
		a.logger.Info("postflight.hostd.rendezvous.dispatched",
			"assignment_id", record.spec.AssignmentID, "member_id", record.spec.MemberID,
			"job_id", record.spec.JobID, "duration_ns", time.Since(started).Nanoseconds())

	case syncproto.AssignmentBinding:
		if status.Phase != vm.PhaseBound {
			return
		}
		a.captureRestore(record, status)
		hostAfterUnixNS := time.Now().UnixNano()
		a.appendTrace(record.trace, record, "mounts_ready", func(event *traceEvent) {
			event.GenerationSet = generationSet(record)
			event.Volumes = traceVolumes(record, true)
			event.Restore = traceRestoreEvidence(record.restore)
		})
		a.appendTrace(record.trace, record, "clock_checked", func(event *traceEvent) {
			event.GenerationSet = generationSet(record)
			event.Clock = &traceClock{
				HostBeforeUnixNS: record.hostBeforeUnixNS, HostAfterUnixNS: hostAfterUnixNS,
				GuestUnixNS: status.Clock.UnixNS, MaxSkewNS: int64(5 * time.Second),
				GuestSynchronized: status.Clock.Synchronized, Clocksource: status.Clock.Clocksource,
				AfterRestore: status.Clock.AfterRestore,
			}
		})
		if record.restore != nil && record.restore.Outcome == string(guestproto.RestoreColdFallback) {
			a.metrics.ColdFallbacks.Add(1)
		}
		if err := a.vms.Authorize(ctx, status.ID, a.authorization(record)); err != nil {
			a.failClosed(ctx, record, "authorize: "+err.Error())
			return
		}
		a.appendTrace(record.trace, record, "worker_authorization_sent", nil)
		record.enter(syncproto.AssignmentAuthorizing, a.now())
		a.logger.Info("postflight.hostd.worker.authorization_sent",
			"assignment_id", record.spec.AssignmentID, "member_id", record.spec.MemberID,
			"job_id", record.spec.JobID)

	case syncproto.AssignmentAuthorizing:
		if status.Phase == vm.PhaseHookBlocked || status.Phase == vm.PhaseReady {
			if err := validateRuntimeIdentity(record.spec.Identity, status.Identity); err != nil {
				a.failClosed(ctx, record, "runtime identity: "+err.Error())
				return
			}
		}
		if status.Phase == vm.PhaseReady {
			a.appendTrace(record.trace, record, "job_hook_released", func(event *traceEvent) {
				event.GenerationSet = generationSet(record)
			})
			record.enter(syncproto.AssignmentRunning, a.now())
			a.logger.Info("postflight.hostd.customer_steps.released",
				"assignment_id", record.spec.AssignmentID, "member_id", record.spec.MemberID,
				"job_id", record.spec.JobID)
		}

	case syncproto.AssignmentRunning:
		// Runner exit is handled above. This state is intentionally otherwise
		// level: customer workload duration belongs to GitHub, not a host timer.

	}
}

func (a *Agent) finalizeExitedAssignment(ctx context.Context, record *assignment) {
	if record.vmID != "" {
		if !a.destroyAssignmentVM(ctx, record) {
			return
		}
	}
	if record.spec.State != syncproto.DesiredAssignmentSeal {
		return
	}
	if record.checkpoint == nil && record.spec.SealCheckpoint != nil {
		checkpoint := *record.spec.SealCheckpoint
		record.checkpoint = &checkpoint
	}
	if record.checkpoint == nil || record.spec.SealGeneration == "" {
		record.reason = "snapshot skipped: checkpoint candidate is incomplete"
		record.enter(syncproto.AssignmentCompleted, a.now())
		return
	}
	a.appendTrace(record.trace, record, "snapshot_seal_started", func(event *traceEvent) {
		event.GenerationSet = generationSet(record)
		event.Checkpoint = &traceCheckpoint{Digest: record.checkpoint.Digest, Version: record.checkpoint.Version}
	})
	set, err := a.zvols.SealSet(ctx, zvol.AssignmentID(record.spec.AssignmentID), zvol.GenerationID(record.spec.SealGeneration))
	if err != nil {
		record.reason = "snapshot skipped: " + err.Error()
		record.enter(syncproto.AssignmentCompleted, a.now())
		return
	}
	record.sealGen = string(set.Workspace.Generation)
	a.appendTrace(record.trace, record, "snapshot_seal_completed", func(event *traceEvent) {
		event.GenerationSet = "workspace:" + string(set.Workspace.Generation) + ",tool:" + string(set.Tool.Generation) + ",process:" + string(set.Process.Generation)
		event.Checkpoint = &traceCheckpoint{Digest: record.checkpoint.Digest, Version: record.checkpoint.Version}
	})
	record.enter(syncproto.AssignmentSealed, a.now())
	a.metrics.SealedGenerations.Add(1)
}

func validateBinding(spec syncproto.DesiredAssignment, status vm.Status) error {
	assignment := status.Assignment
	if assignment.RequestID == "" || assignment.JobID == "" || assignment.CheckRunID <= 0 || assignment.Identity.RunID == "" || assignment.Identity.RunAttempt <= 0 {
		return errors.New("local assignment is incomplete")
	}
	if spec.MemberID != status.Incarnation || status.MemberID != spec.MemberID {
		return errors.New("member incarnation changed")
	}
	if spec.RequestID != assignment.RequestID || spec.JobID != assignment.JobID || spec.CheckRunID != assignment.CheckRunID {
		return errors.New("runner protocol identity changed")
	}
	if spec.Identity.RunnerName != assignment.RunnerName {
		return errors.New("runner name changed")
	}
	return validateRuntimeIdentity(spec.Identity, assignment.Identity)
}

func validateRuntimeIdentity(expected syncproto.JobIdentity, observed vm.JobIdentity) error {
	if expected.RunID != observed.RunID || expected.RunAttempt != observed.RunAttempt ||
		expected.RunnerName != observed.RunnerName || expected.Repository != observed.Repository ||
		expected.WorkflowJob != observed.WorkflowJob {
		return fmt.Errorf("expected run=%s attempt=%d runner=%s repo=%s job=%s; observed run=%s attempt=%d runner=%s repo=%s job=%s",
			expected.RunID, expected.RunAttempt, expected.RunnerName, expected.Repository, expected.WorkflowJob,
			observed.RunID, observed.RunAttempt, observed.RunnerName, observed.Repository, observed.WorkflowJob)
	}
	return nil
}

func (a *Agent) materialize(ctx context.Context, record *assignment) error {
	spec := record.spec
	materializeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workspace zvol.WorkspaceVolume
	var tool zvol.ToolVolume
	var process zvol.ProcessVolume
	errs := make([]error, 3)
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		started := time.Now()
		workspace, errs[0] = a.zvols.EnsureWorkspace(materializeCtx, zvol.AssignmentID(spec.AssignmentID), zvol.GenerationID(spec.Workspace.Generation), spec.Workspace.SizeBytes)
		if errs[0] != nil {
			cancel()
		}
		a.logger.Info("postflight.hostd.volume.materialized", "assignment_id", spec.AssignmentID, "role", "workspace", "duration_ns", time.Since(started).Nanoseconds())
	}()
	go func() {
		defer wait.Done()
		started := time.Now()
		tool, errs[1] = a.zvols.EnsureTool(materializeCtx, zvol.AssignmentID(spec.AssignmentID), zvol.GenerationID(spec.Tool.Generation), toolVolumeSize(spec.Tool.SizeBytes))
		if errs[1] != nil {
			cancel()
		}
		a.logger.Info("postflight.hostd.volume.materialized", "assignment_id", spec.AssignmentID, "role", "tool", "duration_ns", time.Since(started).Nanoseconds())
	}()
	processGeneration := zvol.GenerationID("")
	if spec.Process.ExpectedDigest != "" {
		processGeneration = zvol.GenerationID(spec.Process.Generation)
	}
	processSize := spec.Process.SizeBytes
	if processSize == 0 {
		processSize = defaultProcessVolumeSizeBytes
	}
	go func() {
		defer wait.Done()
		started := time.Now()
		process, errs[2] = a.zvols.EnsureProcess(materializeCtx, zvol.AssignmentID(spec.AssignmentID), processGeneration, processSize)
		if errs[2] != nil {
			cancel()
		}
		a.logger.Info("postflight.hostd.volume.materialized", "assignment_id", spec.AssignmentID, "role", "process", "duration_ns", time.Since(started).Nanoseconds())
	}()
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			return fmt.Errorf("%s: %w", []string{"workspace", "tool", "process"}[index], err)
		}
	}
	record.device, record.volume = workspace.Device, workspace
	record.toolDevice, record.toolVolume = tool.Device, tool
	record.processDevice, record.processVolume = process.Device, process
	return nil
}

func (a *Agent) rendezvous(record *assignment) vm.Rendezvous {
	return vm.Rendezvous{
		MemberID: record.spec.MemberID, AssignmentID: record.spec.AssignmentID,
		WorkspaceDevice: record.device, WorkspaceMountpoint: workspaceMountpoint(record.spec.RepositoryFullName),
		ToolDevice: record.toolDevice, ProcessDevice: record.processDevice,
		CheckpointDigest: record.spec.Process.ExpectedDigest, CheckpointVersion: record.spec.Process.ExpectedVersion,
	}
}

func (a *Agent) authorization(record *assignment) vm.Authorization {
	spec := record.spec
	token := checkoutbundle.DeriveCheckoutToken(a.hostSecret, spec.ExecutionID, spec.AttemptID)
	identity := vm.JobIdentity{
		RunID: spec.Identity.RunID, RunAttempt: spec.Identity.RunAttempt,
		RunnerName: spec.Identity.RunnerName, Repository: spec.Identity.Repository,
		WorkflowJob: spec.Identity.WorkflowJob,
	}
	return vm.Authorization{
		MemberID: spec.MemberID, AssignmentID: spec.AssignmentID, RequestID: spec.RequestID, Identity: identity,
		Env: map[string]string{
			"POSTFLIGHT_HOST_SERVICE_HTTP_ORIGIN": a.cfg.CheckoutGuestOrigin,
			"POSTFLIGHT_CHECKOUT_PATH":            a.cfg.CheckoutPath,
			"POSTFLIGHT_CHECKOUT_TOKEN":           token,
			"POSTFLIGHT_EXECUTION_ID":             spec.ExecutionID,
			"POSTFLIGHT_ATTEMPT_ID":               spec.AttemptID,
			"POSTFLIGHT_WORKSPACE_READY_FILE":     filepath.Join(workspaceMountpoint(spec.RepositoryFullName), guestproto.WorkspaceReadyMarker),
			"RUNNER_TRACKING_ID":                  "",
		},
	}
}

func (a *Agent) captureRestore(record *assignment, status vm.Status) {
	if status.Restore == nil {
		return
	}
	record.restore = &syncproto.RestoreReport{
		Outcome: string(status.Restore.Outcome), ProcessInvalidated: status.Restore.ProcessInvalidated,
		FailureClass: status.Restore.FailureClass, FailureCode: status.Restore.FailureCode,
	}
}

func (a *Agent) finishAssignment(ctx context.Context, record *assignment, status vm.Status) {
	record.exit = status.ExitCode
	a.captureRestore(record, status)
	a.appendTrace(record.trace, record, "runner_exit_observed", func(event *traceEvent) {
		event.GenerationSet = generationSet(record)
	})
	if !status.CustomerStepsReleased {
		a.failClosed(ctx, record, "runner exited before customer steps: "+status.FailureReason)
		return
	}
	if status.ExitCode == 0 && record.device != "" && record.checkpoint == nil && record.reason == "" {
		a.appendTrace(record.trace, record, "checkpoint_started", func(event *traceEvent) {
			event.GenerationSet = generationSet(record)
		})
		artifact, err := a.vms.Quiesce(ctx, status.ID)
		a.appendOriginTiming(record.trace, record, artifact.Timing)
		record.timing = mergeTiming(record.timing, artifact.Timing)
		if err == nil {
			record.checkpoint = &syncproto.CheckpointArtifact{Digest: artifact.Digest, Version: artifact.Version}
			a.appendTrace(record.trace, record, "checkpoint_completed", func(event *traceEvent) {
				event.GenerationSet = generationSet(record)
				event.Checkpoint = &traceCheckpoint{Digest: artifact.Digest, Version: artifact.Version}
			})
		} else {
			record.reason = "snapshot skipped: " + err.Error()
		}
	}
	if !a.destroyAssignmentVM(ctx, record) {
		return
	}
	if record.exit == 0 && record.checkpoint != nil {
		record.enter(syncproto.AssignmentExited, a.now())
	} else {
		record.enter(syncproto.AssignmentCompleted, a.now())
	}
}

func (a *Agent) recycleAndComplete(ctx context.Context, record *assignment, reason string) {
	record.reason = reason
	record.termination = syncproto.AssignmentCompleted
	a.finishPendingTermination(ctx, record)
}

func (a *Agent) markAborted(record *assignment) {
	a.appendTrace(record.trace, record, "assignment_aborted", func(event *traceEvent) {
		event.GenerationSet = generationSet(record)
		event.FailureReason = record.reason
		event.Restore = traceRestoreEvidence(record.restore)
	})
	record.enter(syncproto.AssignmentCompleted, a.now())
	a.logger.Info("postflight.hostd.assignment.aborted", "assignment_id", record.spec.AssignmentID, "member_id", record.spec.MemberID, "job_id", record.spec.JobID, "reason", record.reason)
}

func (a *Agent) failClosed(ctx context.Context, record *assignment, reason string) {
	record.reason = reason
	record.termination = syncproto.AssignmentFailedClosed
	a.finishPendingTermination(ctx, record)
}

func (a *Agent) finishPendingTermination(ctx context.Context, record *assignment) {
	if record.vmID != "" && !a.destroyAssignmentVM(ctx, record) {
		return
	}
	pending := record.termination
	record.termination = ""
	switch pending {
	case syncproto.AssignmentCompleted:
		a.markAborted(record)
	case syncproto.AssignmentFailedClosed:
		a.markFailedClosed(record)
	}
}

func (a *Agent) markFailedClosed(record *assignment) {
	a.appendTrace(record.trace, record, "assignment_failed_closed", func(event *traceEvent) {
		event.GenerationSet = generationSet(record)
		event.FailureReason = record.reason
		event.Restore = traceRestoreEvidence(record.restore)
	})
	record.enter(syncproto.AssignmentFailedClosed, a.now())
	a.metrics.FailedClosedAssignments.Add(1)
	a.logger.Error("postflight.hostd.assignment.failed_closed", "assignment_id", record.spec.AssignmentID, "member_id", record.spec.MemberID, "job_id", record.spec.JobID, "reason", record.reason)
}

func (a *Agent) destroyAssignmentVM(ctx context.Context, record *assignment) bool {
	if record.vmID == "" {
		return true
	}
	destroyedVMID := record.vmID
	a.appendTrace(record.trace, record, "vm_destroy_started", nil)
	if err := a.vms.Destroy(ctx, vm.ID(destroyedVMID)); err != nil {
		a.logger.Error("destroying assignment vm", "assignment_id", record.spec.AssignmentID, "member_id", record.spec.MemberID, "vm", destroyedVMID, "err", err)
		return false
	}
	record.vmID = ""
	a.appendTrace(record.trace, record, "vm_destroy_completed", func(event *traceEvent) {
		event.VMID = destroyedVMID
	})
	return true
}

const runnerWorkRoot = "/home/runner/_work"
const defaultProcessVolumeSizeBytes int64 = 24 << 30
const defaultToolVolumeSizeBytes int64 = 32 << 30

func toolVolumeSize(configured int64) int64 {
	if configured > 0 {
		return configured
	}
	return defaultToolVolumeSizeBytes
}

func workspaceMountpoint(repository string) string {
	name := repository
	if index := strings.LastIndexByte(name, '/'); index >= 0 {
		name = name[index+1:]
	}
	return runnerWorkRoot + "/" + name + "/" + name
}

func (a *Agent) reapGenerations(ctx context.Context, desiredAssignments map[string]syncproto.DesiredAssignment, reap []zvol.GenerationID) {
	referenced := map[zvol.GenerationID]bool{}
	for _, desired := range desiredAssignments {
		if desired.Workspace.Generation != "" {
			referenced[zvol.GenerationID(desired.Workspace.Generation)] = true
		}
	}
	for _, generation := range reap {
		if referenced[generation] {
			continue
		}
		err := a.zvols.DestroyProcessGeneration(ctx, generation)
		if errors.Is(err, zvol.ErrNotFound) {
			err = nil
		}
		if err == nil {
			err = a.zvols.DestroyToolGeneration(ctx, generation)
		}
		if errors.Is(err, zvol.ErrNotFound) {
			err = nil
		}
		if err == nil {
			err = a.zvols.DestroyGeneration(ctx, generation)
		}
		if err == nil {
			a.metrics.ReapedGenerations.Add(1)
		} else if !errors.Is(err, zvol.ErrNotFound) && !errors.Is(err, zvol.ErrBusy) {
			a.logger.Error("reaping generation", "generation", generation, "err", err)
		}
	}
}

func sortedAssignmentIDs(assignments map[string]*assignment) []string {
	ids := make([]string, 0, len(assignments))
	for id := range assignments {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortVMIDs(ids []vm.ID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
}

func workspaceAssignment(name string) string {
	if index := strings.LastIndex(name, "/ws/"); index >= 0 {
		return name[index+len("/ws/"):]
	}
	return ""
}

func mergeTiming(existing []syncproto.TimingPoint, incoming []vm.TimingPoint) []syncproto.TimingPoint {
	seen := map[string]bool{}
	for _, point := range existing {
		seen[fmt.Sprintf("%s/%s/%d", point.Source, point.BootID, point.Sequence)] = true
	}
	for _, point := range incoming {
		key := fmt.Sprintf("%s/%s/%d", point.Source, point.BootID, point.Sequence)
		if seen[key] {
			continue
		}
		seen[key] = true
		existing = append(existing, syncproto.TimingPoint{
			Event: point.Event, Source: point.Source, BootID: point.BootID, Sequence: point.Sequence,
			MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS,
		})
	}
	return existing
}
