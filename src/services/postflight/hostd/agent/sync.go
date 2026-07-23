package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func validateMember(spec syncproto.DesiredPoolMember) error {
	if err := zvol.ValidateName("member", spec.MemberID); err != nil {
		return err
	}
	if spec.VMID == "" || spec.RunnerClass == "" || spec.RunnerName == "" {
		return errors.New("member VM, class, and runner name are required")
	}
	if err := zvol.ValidateName("runner", spec.RunnerName); err != nil {
		return err
	}
	if spec.State != syncproto.DesiredMemberListen && spec.State != syncproto.DesiredMemberRecycle {
		return fmt.Errorf("unknown member state %q", spec.State)
	}
	return nil
}

func validateAssignment(spec syncproto.DesiredAssignment) error {
	for label, value := range map[string]string{
		"assignment": spec.AssignmentID, "member": spec.MemberID,
	} {
		if err := zvol.ValidateName(label, value); err != nil {
			return err
		}
	}
	if spec.RequestID == "" || spec.JobID == "" || spec.CheckRunID <= 0 || spec.RunnerClass == "" {
		return errors.New("assignment protocol identity and class are required")
	}
	if spec.State != syncproto.DesiredAssignmentRun && spec.State != syncproto.DesiredAssignmentSeal && spec.State != syncproto.DesiredAssignmentAbort {
		return fmt.Errorf("unknown assignment state %q", spec.State)
	}
	if spec.Identity.RunID == "" || spec.Identity.RunAttempt <= 0 || spec.Identity.RunnerName == "" || spec.Identity.Repository == "" || spec.Identity.WorkflowJob == "" {
		return errors.New("assignment runtime identity is incomplete")
	}
	if spec.RepositoryFullName == "" || spec.RepositoryFullName != spec.Identity.Repository {
		return errors.New("assignment repository identity differs")
	}
	if spec.Process.ExpectedDigest != "" || spec.Process.ExpectedVersion != "" || spec.Process.Generation != "" {
		if spec.Process.ExpectedDigest == "" || spec.Process.ExpectedVersion == "" || spec.Process.Generation == "" {
			return errors.New("process snapshot identity is incomplete")
		}
	}
	if spec.State == syncproto.DesiredAssignmentSeal && (spec.SealGeneration == "" || spec.SealCheckpoint == nil) {
		return errors.New("seal assignment is incomplete")
	}
	return nil
}

func immutableAssignmentDifference(left, right syncproto.DesiredAssignment) string {
	checks := []struct {
		name  string
		equal bool
	}{
		{"assignment_id", left.AssignmentID == right.AssignmentID},
		{"member_id", left.MemberID == right.MemberID},
		{"runner_protocol", left.RequestID == right.RequestID && left.JobID == right.JobID && left.CheckRunID == right.CheckRunID},
		{"execution", left.ExecutionID == right.ExecutionID && left.AttemptID == right.AttemptID},
		{"repository", left.RepositoryFullName == right.RepositoryFullName},
		{"runner_class", left.RunnerClass == right.RunnerClass},
		{"identity", left.Identity == right.Identity},
		{"workspace", left.Workspace == right.Workspace},
		{"tool", left.Tool == right.Tool},
		{"process", left.Process == right.Process},
	}
	for _, check := range checks {
		if !check.equal {
			return check.name
		}
	}
	return ""
}

func (a *Agent) applyDesired(response syncproto.SyncResponse) {
	if response.BootID != a.bootID {
		a.logger.Error("dropping sync response with wrong boot id", "got", response.BootID, "want", a.bootID)
		return
	}
	members := make(map[string]syncproto.DesiredPoolMember, len(response.Members))
	quarantinedMembers := map[string]bool{}
	for _, spec := range response.Members {
		if err := validateMember(spec); err != nil {
			a.metrics.RejectedMembers.Add(1)
			quarantinedMembers[spec.MemberID] = true
			a.logger.Error("rejecting desired pool member", "member_id", spec.MemberID, "err", err)
			continue
		}
		if _, duplicate := members[spec.MemberID]; duplicate {
			quarantinedMembers[spec.MemberID] = true
			delete(members, spec.MemberID)
			continue
		}
		members[spec.MemberID] = spec
	}

	desiredAssignments := make(map[string]syncproto.DesiredAssignment, len(response.Assignments))
	quarantinedJobs := map[string]bool{}
	for _, spec := range response.Assignments {
		if err := validateAssignment(spec); err != nil {
			a.metrics.RejectedAssignments.Add(1)
			quarantinedJobs[spec.AssignmentID] = true
			a.logger.Error("rejecting desired assignment", "assignment_id", spec.AssignmentID, "err", err)
			continue
		}
		if _, duplicate := desiredAssignments[spec.AssignmentID]; duplicate {
			quarantinedJobs[spec.AssignmentID] = true
			delete(desiredAssignments, spec.AssignmentID)
			continue
		}
		desiredAssignments[spec.AssignmentID] = spec
		record := a.assignments[spec.AssignmentID]
		if record == nil {
			point := a.timing.Point("assignment_update_received")
			record = &assignment{
				memberID: spec.MemberID,
				spec:     spec, state: syncproto.AssignmentObserved, since: a.now(),
				updateTiming: vm.TimingPoint{
					Event: point.Event, Source: point.Source, BootID: point.BootID,
					Sequence: point.Sequence, MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS,
				},
			}
			if member, ok := members[spec.MemberID]; ok {
				trace, err := a.traceFor(spec.MemberID, member.RunnerName, member.VMID)
				if err != nil {
					a.logger.Error("opening assignment evidence", "assignment_id", spec.AssignmentID, "member_id", spec.MemberID, "err", err)
				} else {
					record.trace = trace
				}
			}
			a.assignments[spec.AssignmentID] = record
			continue
		}
		record.mu.Lock()
		if difference := immutableAssignmentDifference(record.spec, spec); difference != "" {
			quarantinedJobs[spec.AssignmentID] = true
			a.metrics.RejectedAssignments.Add(1)
			a.logger.Error("rejecting mutation of immutable assignment", "assignment_id", spec.AssignmentID, "field", difference,
				"local_workspace", record.spec.Workspace, "desired_workspace", spec.Workspace)
			record.mu.Unlock()
			continue
		}
		record.spec.State = spec.State
		record.spec.SealGeneration = spec.SealGeneration
		record.spec.SealCheckpoint = spec.SealCheckpoint
		record.mu.Unlock()
	}

	a.desiredMembers = members
	a.desiredAssignments = desiredAssignments
	for id, record := range a.assignments {
		record.mu.Lock()
		if record.state == syncproto.AssignmentExited {
			if _, stillDesired := desiredAssignments[id]; !stillDesired {
				record.enter(syncproto.AssignmentCompleted, a.now())
			}
		}
		record.mu.Unlock()
	}
	a.quarantinedMembers = quarantinedMembers
	a.quarantinedJobs = quarantinedJobs
	a.reap = a.reap[:0]
	for _, generation := range response.Reap {
		if err := zvol.ValidateName("generation", generation); err != nil {
			a.logger.Error("rejecting reap generation", "generation", generation, "err", err)
			continue
		}
		a.reap = append(a.reap, zvol.GenerationID(generation))
	}
	a.poolTargets = map[vm.Class]int{}
	for class, count := range response.PoolTargets {
		if count >= 0 {
			a.poolTargets[vm.Class(class)] = count
		}
	}
	a.synced = true
}

func (a *Agent) buildReport(ctx context.Context) (syncproto.SyncRequest, error) {
	a.mu.Lock()
	desiredMembers := cloneMap(a.desiredMembers)
	assignments := cloneMap(a.assignments)
	a.mu.Unlock()
	request := syncproto.SyncRequest{
		HostID: a.cfg.HostID, BootID: a.bootID,
		Platform: syncproto.PlatformReport{
			QEMUVersion: a.cfg.Platform.QEMUVersion, KernelRelease: a.cfg.Platform.KernelRelease,
			OSImageID: a.cfg.Platform.OSImageID, MachineType: a.cfg.Platform.MachineType,
			CPUModel: a.cfg.Platform.CPUModel, CRIUVersion: a.cfg.Platform.CRIUVersion,
		},
	}
	statuses, err := a.vms.List(ctx)
	if err != nil {
		return request, err
	}
	slots := map[vm.Class]*syncproto.SlotReport{}
	for class, total := range a.cfg.Slots {
		slots[class] = &syncproto.SlotReport{Class: string(class), Total: total}
	}
	for _, status := range statuses {
		slot := slots[status.Class]
		if slot == nil {
			slot = &syncproto.SlotReport{Class: string(status.Class)}
			slots[status.Class] = slot
		}
		switch status.Phase {
		case vm.PhaseBooting, vm.PhaseWarm, vm.PhaseAssigned:
			slot.Booting++
		case vm.PhaseListening:
			slot.Listening++
		default:
			slot.Busy++
		}
		if status.Incarnation == "" {
			continue
		}
		member := syncproto.PoolMemberReport{
			MemberID: status.Incarnation, VMID: string(status.ID), Class: string(status.Class),
			Image: status.Image, State: memberState(status),
		}
		if desired, ok := desiredMembers[status.Incarnation]; ok {
			member.RunnerName = desired.RunnerName
		}
		if status.Assignment.RequestID != "" {
			member.RunnerName = status.Assignment.RunnerName
			member.Assignment = observedAssignment(status.Assignment)
		}
		if status.Phase == vm.PhaseRecycleRequired {
			member.Reason = status.FailureReason
		}
		request.Members = append(request.Members, member)
	}
	for _, slot := range slots {
		request.Slots = append(request.Slots, *slot)
	}
	for _, record := range assignments {
		record.mu.Lock()
		request.Assignments = append(request.Assignments, record.report())
		record.mu.Unlock()
	}
	generations, workspaces, err := a.zvols.Inventory(ctx)
	if err != nil {
		return request, err
	}
	for _, generation := range generations {
		request.Generations = append(request.Generations, syncproto.GenerationReport{Generation: string(generation.Generation), Bytes: generation.Bytes})
	}
	for _, workspace := range workspaces {
		request.Workspaces = append(request.Workspaces, workspace.Name)
	}
	sort.Slice(request.Slots, func(i, j int) bool { return request.Slots[i].Class < request.Slots[j].Class })
	sort.Slice(request.Members, func(i, j int) bool { return request.Members[i].MemberID < request.Members[j].MemberID })
	sort.Slice(request.Assignments, func(i, j int) bool { return request.Assignments[i].AssignmentID < request.Assignments[j].AssignmentID })
	sort.Slice(request.Generations, func(i, j int) bool { return request.Generations[i].Generation < request.Generations[j].Generation })
	sort.Strings(request.Workspaces)
	return request, nil
}

func memberState(status vm.Status) syncproto.PoolMemberState {
	switch status.Phase {
	case vm.PhaseBooting:
		return syncproto.MemberProvisioning
	case vm.PhaseWarm:
		return syncproto.MemberWarm
	case vm.PhaseAssigned:
		return syncproto.MemberPreparing
	case vm.PhaseListening:
		return syncproto.MemberListening
	case vm.PhaseJobAssigned:
		return syncproto.MemberAssigned
	case vm.PhaseBound, vm.PhaseWorkerReady, vm.PhaseHookBlocked:
		return syncproto.MemberRendezvous
	case vm.PhaseReady:
		return syncproto.MemberRunning
	case vm.PhaseRecycleRequired, vm.PhaseExited:
		return syncproto.MemberRecycling
	default:
		return syncproto.MemberLost
	}
}

func observedAssignment(input vm.Assignment) *syncproto.ObservedAssignment {
	timing := make([]syncproto.TimingPoint, 0, len(input.Timing))
	for _, point := range input.Timing {
		timing = append(timing, syncproto.TimingPoint{
			Event: point.Event, Source: point.Source, BootID: point.BootID, Sequence: point.Sequence,
			MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS,
		})
	}
	return &syncproto.ObservedAssignment{
		RequestID: input.RequestID, JobID: input.JobID, CheckRunID: input.CheckRunID, RunnerName: input.RunnerName,
		JobDisplayName: input.JobDisplayName,
		Identity: syncproto.JobIdentity{
			RunID: input.Identity.RunID, RunAttempt: input.Identity.RunAttempt,
			RunnerName: input.Identity.RunnerName, Repository: input.Identity.Repository,
			WorkflowJob: input.Identity.WorkflowJob,
		},
		Timing: timing,
	}
}

func (a *Agent) syncOnce(ctx context.Context) (time.Duration, error) {
	started := time.Now()
	request, err := a.buildReport(ctx)
	if err != nil {
		return 0, fmt.Errorf("build report: %w", err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	endpoint, err := url.JoinPath(a.cfg.ControlPlaneOrigin, syncproto.SyncPath)
	if err != nil {
		return 0, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+a.credential)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := a.httpClient.Do(httpRequest)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("control plane returned %s: %s", response.Status, detail)
	}
	var desired syncproto.SyncResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&desired); err != nil {
		return 0, err
	}
	a.mu.Lock()
	a.applyDesired(desired)
	a.mu.Unlock()
	a.retryDeferredUpdates(ctx)
	a.logger.Info("postflight.hostd.sync.completed", "duration_ns", time.Since(started).Nanoseconds(),
		"reported_members", len(request.Members), "reported_assignments", len(request.Assignments),
		"desired_members", len(desired.Members), "desired_assignments", len(desired.Assignments))
	if desired.PollAfterMillis > 0 {
		return time.Duration(desired.PollAfterMillis) * time.Millisecond, nil
	}
	return 0, nil
}
