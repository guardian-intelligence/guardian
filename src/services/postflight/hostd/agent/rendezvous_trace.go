package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

const rendezvousTraceSchema = 7

type traceState struct {
	memberID   string
	runnerName string
	vmID       string
	seq        uint64
	seen       map[string]bool
	file       *os.File
}

type traceEvent struct {
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
	JobID         int64  `json:"job_id,omitempty"`
	CheckRunID    int64  `json:"check_run_id,omitempty"`
	RunAttempt    int    `json:"run_attempt,omitempty"`
	RunnerName    string `json:"runner_name,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	RunnerJobID   string `json:"runner_job_id,omitempty"`
	VMID          string `json:"vm_id,omitempty"`
	GenerationSet string `json:"generation_set,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`

	MemberID     string `json:"member_id,omitempty"`
	AssignmentID string `json:"assignment_id,omitempty"`

	Volumes    []traceVolume    `json:"volumes,omitempty"`
	Platform   *tracePlatform   `json:"platform,omitempty"`
	Clock      *traceClock      `json:"clock,omitempty"`
	Checkpoint *traceCheckpoint `json:"checkpoint,omitempty"`
	Restore    *traceRestore    `json:"restore,omitempty"`
}

type traceVolume struct {
	Role            string `json:"role"`
	Dataset         string `json:"dataset"`
	Materialization string `json:"materialization"`
	SnapshotGUID    string `json:"snapshot_guid"`
	Generation      string `json:"generation"`
	DeviceSerial    string `json:"device_serial,omitempty"`
}

type tracePlatform struct {
	QEMUVersion   string `json:"qemu_version"`
	KernelRelease string `json:"kernel_release"`
	OSImageID     string `json:"os_image_id"`
	MachineType   string `json:"machine_type"`
	CPUModel      string `json:"cpu_model"`
	CRIUVersion   string `json:"criu_version"`
}

type traceClock struct {
	HostBeforeUnixNS  int64  `json:"host_before_unix_ns"`
	HostAfterUnixNS   int64  `json:"host_after_unix_ns"`
	GuestUnixNS       int64  `json:"guest_unix_ns"`
	MaxSkewNS         int64  `json:"max_skew_ns"`
	GuestSynchronized bool   `json:"guest_synchronized"`
	Clocksource       string `json:"clocksource"`
	AfterRestore      bool   `json:"after_restore"`
}

type traceCheckpoint struct {
	Digest  string `json:"digest"`
	Version string `json:"version"`
}

type traceRestore struct {
	Outcome            string `json:"outcome"`
	ProcessInvalidated bool   `json:"process_invalidated,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
	FailureCode        string `json:"failure_code,omitempty"`
}

func (a *Agent) traceFor(memberID, runnerName, vmID string) (*traceState, error) {
	if a.cfg.TraceDir == "" {
		return nil, nil
	}
	if err := zvol.ValidateName("member", memberID); err != nil {
		return nil, err
	}
	if err := zvol.ValidateName("runner", runnerName); err != nil {
		return nil, err
	}
	if state := a.traces[memberID]; state != nil {
		if state.runnerName != runnerName || (state.vmID != "" && vmID != "" && state.vmID != vmID) {
			return nil, fmt.Errorf("trace identity changed for member %s", memberID)
		}
		if state.vmID == "" {
			state.vmID = vmID
		}
		return state, nil
	}
	state := &traceState{
		memberID: memberID, runnerName: runnerName, vmID: vmID,
		seen: map[string]bool{},
	}
	path := filepath.Join(a.cfg.TraceDir, runnerName+".jsonl")
	if err := state.adopt(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	state.file = file
	a.traces[memberID] = state
	return state, nil
}

func (s *traceState) adopt(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var event traceEvent
		if err := decoder.Decode(&event); err != nil {
			return fmt.Errorf("trace line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return fmt.Errorf("trace line %d contains more than one JSON value", line)
		}
		if event.SchemaVersion != rendezvousTraceSchema || event.Seq <= s.seq || event.MemberID != s.memberID {
			return fmt.Errorf("trace line %d has incompatible schema, sequence, or member", line)
		}
		if event.RunnerName != "" && event.RunnerName != s.runnerName {
			return fmt.Errorf("trace line %d changes runner identity", line)
		}
		if event.VMID != "" && s.vmID != "" && event.VMID != s.vmID {
			return fmt.Errorf("trace line %d changes VM identity", line)
		}
		s.seq = event.Seq
		s.seen[event.Event] = true
	}
	return scanner.Err()
}

func (a *Agent) appendOriginTiming(state *traceState, record *assignment, points []vm.TimingPoint) {
	for _, point := range points {
		point := point
		a.appendTrace(state, record, point.Event, func(event *traceEvent) {
			event.Source = point.Source
			event.BootID = point.BootID
			event.OriginSeq = point.Sequence
			event.MonotonicNS = point.MonotonicNS
			event.WallTime = time.Unix(0, point.UnixNS).UTC()
		})
	}
}

func (a *Agent) appendBootstrapTiming(state *traceState, points []vm.TimingPoint) {
	for _, point := range points {
		if !preAssignmentTraceEvent(point.Event) {
			continue
		}
		a.appendOriginTiming(state, nil, []vm.TimingPoint{point})
	}
}

func preAssignmentTraceEvent(event string) bool {
	switch event {
	case "vm_launch_started", "qemu_started", "guest_hello_observed",
		"listener_prepare_started", "listener_prepare_sent",
		"listener_prepare_received", "runner_registered", "pool_ready":
		return true
	default:
		return false
	}
}

func (a *Agent) appendTrace(state *traceState, record *assignment, event string, enrich func(*traceEvent)) {
	if state == nil || state.file == nil || state.seen[event] {
		return
	}
	point := a.timing.Point(event)
	observation := traceEvent{
		SchemaVersion: rendezvousTraceSchema, Event: event, Seq: state.seq + 1,
		Source: point.Source, BootID: point.BootID, OriginSeq: point.Sequence,
		MonotonicNS: point.MonotonicNS, WallTime: time.Unix(0, point.UnixNS).UTC(),
		RunnerName: state.runnerName, VMID: state.vmID, MemberID: state.memberID,
	}
	if record != nil && !preAssignmentTraceEvent(event) {
		traceAssignment(record, &observation)
	}
	if enrich != nil {
		enrich(&observation)
	}
	raw, err := json.Marshal(observation)
	if err == nil {
		raw = append(raw, '\n')
		_, err = state.file.Write(raw)
	}
	if err != nil {
		a.logger.Error("writing rendezvous evidence", "member_id", state.memberID, "assignment_id", observation.AssignmentID, "event", event, "err", err)
		return
	}
	state.seq = observation.Seq
	state.seen[event] = true
	if event == "vm_destroy_completed" || event == "assignment_requeued" || event == "assignment_failed_closed" || event == "snapshot_seal_completed" {
		if err := state.file.Sync(); err != nil {
			a.logger.Error("syncing terminal rendezvous evidence", "member_id", state.memberID, "event", event, "err", err)
		}
	}
	a.logger.Info("postflight.hostd.rendezvous.evidence", "member_id", state.memberID, "assignment_id", observation.AssignmentID, "event", event, "seq", state.seq)
}

func (a *Agent) closeTraceFiles() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for memberID, state := range a.traces {
		if state.file != nil {
			if err := state.file.Close(); err != nil {
				a.logger.Error("closing rendezvous evidence", "member_id", memberID, "err", err)
			}
		}
		delete(a.traces, memberID)
	}
}

func (a *Agent) pruneTraces() {
	live := make(map[string]bool, len(a.desiredMembers)+len(a.assignments))
	for memberID := range a.desiredMembers {
		live[memberID] = true
	}
	for _, record := range a.assignments {
		live[record.spec.MemberID] = true
	}
	for memberID, state := range a.traces {
		if live[memberID] {
			continue
		}
		if state.file != nil {
			if err := state.file.Close(); err != nil {
				a.logger.Error("closing retired rendezvous evidence", "member_id", memberID, "err", err)
			}
		}
		delete(a.traces, memberID)
	}
}

func (a *Agent) platformEvidence() *tracePlatform {
	p := a.cfg.Platform
	return &tracePlatform{
		QEMUVersion: p.QEMUVersion, KernelRelease: p.KernelRelease,
		OSImageID: p.OSImageID, MachineType: p.MachineType,
		CPUModel: p.CPUModel, CRIUVersion: p.CRIUVersion,
	}
}

func traceAssignment(record *assignment, event *traceEvent) {
	spec := record.spec
	event.RunID = spec.Identity.RunID
	event.JobID, _ = strconv.ParseInt(spec.ExecutionID, 10, 64)
	event.CheckRunID = spec.CheckRunID
	event.RunAttempt = spec.Identity.RunAttempt
	event.RunnerName = spec.Identity.RunnerName
	event.RequestID = spec.RequestID
	event.RunnerJobID = spec.JobID
	event.MemberID = spec.MemberID
	event.AssignmentID = spec.AssignmentID
	event.Repo = spec.RepositoryFullName
	if record.vmID != "" {
		event.VMID = record.vmID
	}
}

func generationSet(record *assignment) string {
	return generationComponent("workspace", record.volume) + "," +
		generationComponent("tool", record.toolVolume) + "," +
		generationComponent("process", record.processVolume)
}

func generationComponent(role string, volume zvol.WorkspaceVolume) string {
	if volume.Source == "" {
		return role + ":empty"
	}
	return role + ":" + string(volume.Source) + ":" + volume.SourceSnapshotGUID
}

func traceVolumes(record *assignment, bound bool) []traceVolume {
	return []traceVolume{
		traceVolumeFor("workspace", "workspace", record.volume, bound),
		traceVolumeFor("tool", "tool", record.toolVolume, bound),
		traceVolumeFor("process", "process", record.processVolume, bound),
	}
}

func traceVolumeFor(role, serial string, volume zvol.WorkspaceVolume, bound bool) traceVolume {
	materialization := "empty"
	if volume.Source != "" {
		materialization = "clone"
	}
	if !bound {
		serial = ""
	}
	return traceVolume{
		Role: role, Dataset: volume.Name, Materialization: materialization,
		SnapshotGUID: volume.SourceSnapshotGUID, Generation: string(volume.Source),
		DeviceSerial: serial,
	}
}

func traceRestoreEvidence(report *syncproto.RestoreReport) *traceRestore {
	if report == nil {
		return nil
	}
	return &traceRestore{
		Outcome: report.Outcome, ProcessInvalidated: report.ProcessInvalidated,
		FailureClass: report.FailureClass, FailureCode: report.FailureCode,
	}
}
