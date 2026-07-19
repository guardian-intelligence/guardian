package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const rendezvousTraceSchema = 2

type traceEvent struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Event         string    `json:"event"`
	Seq           uint64    `json:"seq"`
	Source        string    `json:"source"`
	MonotonicNS   int64     `json:"monotonic_ns"`
	WallTime      time.Time `json:"wall_time"`

	Repo          string `json:"repo,omitempty"`
	JobID         int64  `json:"job_id,omitempty"`
	RunAttempt    int    `json:"run_attempt,omitempty"`
	RunnerName    string `json:"runner_name,omitempty"`
	VMID          string `json:"vm_id,omitempty"`
	GenerationSet string `json:"generation_set,omitempty"`

	Volumes  []traceVolume  `json:"volumes,omitempty"`
	Platform *tracePlatform `json:"platform,omitempty"`
	Clock    *traceClock    `json:"clock,omitempty"`
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

func (a *Agent) appendTrace(record *lease, event string, enrich func(*traceEvent)) {
	if a.cfg.TraceDir == "" {
		return
	}
	record.traceSeq++
	observation := traceEvent{
		SchemaVersion: rendezvousTraceSchema,
		RunID:         strconv.FormatInt(record.spec.ProviderRunID, 10),
		Event:         event,
		Seq:           record.traceSeq,
		Source:        "host:" + a.cfg.HostID,
		MonotonicNS:   time.Since(a.started).Nanoseconds() + 1,
		WallTime:      time.Now().UTC(),
	}
	if enrich != nil {
		enrich(&observation)
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		a.logger.Error("encoding rendezvous evidence", "lease", record.spec.LeaseID, "event", event, "err", err)
		return
	}
	path := filepath.Join(a.cfg.TraceDir, record.spec.LeaseID+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		a.logger.Error("opening rendezvous evidence", "lease", record.spec.LeaseID, "event", event, "err", err)
		return
	}
	if _, err := fmt.Fprintf(file, "%s\n", raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		a.logger.Error("writing rendezvous evidence", "lease", record.spec.LeaseID, "event", event, "err", err)
		return
	}
	a.logger.Info("rendezvous evidence", "lease", record.spec.LeaseID, "event", event, "seq", record.traceSeq)
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
	event.JobID = record.spec.ProviderJobID
	event.RunAttempt = record.spec.ProviderRunAttempt
	event.RunnerName = record.spec.AssignedRunnerName
	event.VMID = record.vmID
}

func generationSet(record *lease) string {
	if record.volume.Source == "" {
		return "workspace:empty"
	}
	return "workspace:" + string(record.volume.Source) + ":" + record.volume.SourceSnapshotGUID
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
	return []traceVolume{{
		Role: "workspace", Dataset: record.volume.Name, Materialization: materialization,
		SnapshotGUID: record.volume.SourceSnapshotGUID, Generation: string(record.volume.Source),
		DeviceSerial: serial,
	}}
}
