package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
)

const rendezvousTraceSchema = 6

type traceEvent struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id,omitempty"`
	Event         string    `json:"event"`
	Seq           uint64    `json:"seq"`
	Source        string    `json:"source"`
	BootID        string    `json:"boot_id,omitempty"`
	OriginSeq     uint64    `json:"origin_seq,omitempty"`
	MonotonicNS   int64     `json:"monotonic_ns"`
	WallTime      time.Time `json:"wall_time"`

	Repo          string `json:"repo,omitempty"`
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

	Volumes    []traceVolume    `json:"volumes,omitempty"`
	Platform   *tracePlatform   `json:"platform,omitempty"`
	Clock      *traceClock      `json:"clock,omitempty"`
	Checkpoint *traceCheckpoint `json:"checkpoint,omitempty"`
}

func (a *Agent) appendOriginTiming(record *lease, points []vm.TimingPoint) {
	for _, point := range points {
		point := point
		a.appendTrace(record, point.Event, func(event *traceEvent) {
			event.Source = point.Source
			event.BootID = point.BootID
			event.OriginSeq = point.Sequence
			event.MonotonicNS = point.MonotonicNS
			event.WallTime = time.Unix(0, point.UnixNS).UTC()
			if preAssignmentTiming(point.Event) {
				event.RunID = ""
				event.ExecutionLeaseID = ""
				event.RunnerName = record.spec.LeaseID
				event.VMID = record.vmID
			} else {
				traceAssignment(record, event)
			}
		})
	}
}

func preAssignmentTiming(event string) bool {
	switch event {
	case "vm_launch_started", "qemu_started", "guest_hello_observed",
		"listener_prepare_started", "listener_prepare_sent",
		"listener_prepare_received", "runner_registered":
		return true
	default:
		return false
	}
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
	CRIUVersion   string `json:"criu_version,omitempty"`
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

func (a *Agent) appendTrace(record *lease, event string, enrich func(*traceEvent)) {
	if a.cfg.TraceDir == "" {
		return
	}
	if record.traceSeen == nil {
		record.traceSeen = map[string]bool{}
	}
	if record.traceSeen[event] {
		return
	}
	record.traceSeq++
	observation := a.newTraceEvent(record, event)
	observation.Seq = record.traceSeq
	if enrich != nil {
		enrich(&observation)
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		a.logger.Error("encoding rendezvous evidence", "lease", record.spec.LeaseID, "event", event, "err", err)
		return
	}
	file, err := a.traceFile(record.spec.LeaseID)
	if err == nil {
		raw = append(raw, '\n')
		_, err = file.Write(raw)
	}
	if err != nil {
		a.logger.Error("writing rendezvous evidence", "lease", record.spec.LeaseID, "event", event, "err", err)
		return
	}
	record.traceSeen[event] = true
	a.logger.Info("rendezvous evidence", "lease", record.spec.LeaseID, "event", event, "seq", record.traceSeq)
}

func (a *Agent) traceFile(leaseID string) (*os.File, error) {
	if file := a.traceFiles[leaseID]; file != nil {
		return file, nil
	}
	path := filepath.Join(a.cfg.TraceDir, leaseID+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	a.traceFiles[leaseID] = file
	return file, nil
}

func (a *Agent) closeTraceFiles() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for leaseID, file := range a.traceFiles {
		if err := file.Close(); err != nil {
			a.logger.Error("closing rendezvous evidence", "lease", leaseID, "err", err)
		}
		delete(a.traceFiles, leaseID)
	}
}

func (a *Agent) newTraceEvent(record *lease, event string) traceEvent {
	wallTime := time.Now().UTC()
	source := "host:" + a.cfg.HostID
	bootID := ""
	originSeq := uint64(0)
	monotonicNS := time.Since(a.started).Nanoseconds() + 1
	if a.timing != nil {
		point := a.timing.Point(event)
		source = point.Source
		bootID = point.BootID
		originSeq = point.Sequence
		monotonicNS = point.MonotonicNS
		wallTime = time.Unix(0, point.UnixNS).UTC()
	}
	observation := traceEvent{
		SchemaVersion:   rendezvousTraceSchema,
		Event:           event,
		Source:          source,
		BootID:          bootID,
		OriginSeq:       originSeq,
		MonotonicNS:     monotonicNS,
		WallTime:        wallTime,
		ListenerLeaseID: record.spec.LeaseID,
	}
	if event != "pool_ready" && record.assignment != nil {
		execution := record.executionSpec()
		observation.RunID = strconv.FormatInt(execution.ProviderRunID, 10)
		observation.ExecutionLeaseID = record.executionLeaseID()
	}
	return observation
}

func (a *Agent) platformEvidence() *tracePlatform {
	p := a.cfg.Platform
	return &tracePlatform{
		QEMUVersion: p.QEMUVersion, KernelRelease: p.KernelRelease,
		OSImageID: p.OSImageID, MachineType: p.MachineType,
		CPUModel: p.CPUModel, CRIUVersion: p.CRIUVersion,
	}
}

func traceIdentity(record *lease, event *traceEvent) {
	execution := record.executionSpec()
	event.JobID = execution.ProviderJobID
	event.RunAttempt = execution.ProviderRunAttempt
	if record.assignment != nil {
		event.RunnerName = record.assignment.RunnerName
	} else {
		event.RunnerName = record.spec.LeaseID
	}
	event.VMID = record.vmID
}

func traceAssignment(record *lease, event *traceEvent) {
	traceIdentity(record, event)
	if record.assignment != nil {
		event.RequestID = record.assignment.RequestID
		event.RunnerJobID = record.assignment.JobID
	}
}

func generationSet(record *lease) string {
	if record.volume.Source == "" {
		return "workspace:empty,tool:empty,process:empty"
	}
	tool := "tool:empty"
	if record.toolVolume.Source != "" {
		tool = "tool:" + string(record.toolVolume.Source) + ":" + record.toolVolume.SourceSnapshotGUID
	}
	process := "process:empty"
	if record.processVolume.Source != "" {
		process = "process:" + string(record.processVolume.Source) + ":" + record.processVolume.SourceSnapshotGUID
	}
	return "workspace:" + string(record.volume.Source) + ":" + record.volume.SourceSnapshotGUID + "," + tool + "," + process
}

func traceVolumes(record *lease, bound bool) []traceVolume {
	serial := ""
	if bound {
		serial = "workspace"
	}
	materialization := "empty"
	if record.volume.Source != "" {
		materialization = "clone"
	}
	processSerial := ""
	if bound {
		processSerial = "process"
	}
	processMaterialization := "empty"
	if record.processVolume.Source != "" {
		processMaterialization = "clone"
	}
	toolSerial := ""
	if bound {
		toolSerial = "tool"
	}
	toolMaterialization := "empty"
	if record.toolVolume.Source != "" {
		toolMaterialization = "clone"
	}
	return []traceVolume{{
		Role: "workspace", Dataset: record.volume.Name, Materialization: materialization,
		SnapshotGUID: record.volume.SourceSnapshotGUID, Generation: string(record.volume.Source),
		DeviceSerial: serial,
	}, {
		Role: "tool", Dataset: record.toolVolume.Name, Materialization: toolMaterialization,
		SnapshotGUID: record.toolVolume.SourceSnapshotGUID, Generation: string(record.toolVolume.Source),
		DeviceSerial: toolSerial,
	}, {
		Role: "process", Dataset: record.processVolume.Name, Materialization: processMaterialization,
		SnapshotGUID: record.processVolume.SourceSnapshotGUID, Generation: string(record.processVolume.Source),
		DeviceSerial: processSerial,
	}}
}
