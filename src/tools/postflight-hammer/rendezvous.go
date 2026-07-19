package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

const rendezvousTraceSchema = 2

const (
	eventPoolReady                  = "pool_ready"
	eventAssignmentObserved         = "assignment_observed"
	eventJobHookBlocked             = "job_hook_blocked"
	eventJobIdentityReported        = "job_identity_reported"
	eventGenerationResolved         = "generation_resolved"
	eventRendezvousBound            = "rendezvous_bound"
	eventMountsReady                = "mounts_ready"
	eventClockChecked               = "clock_checked"
	eventJobHookReleased            = "job_hook_released"
	eventRunnerExited               = "runner_exited"
	eventSnapshotDecided            = "snapshot_decided"
	eventSnapshotSealed             = "snapshot_sealed"
	eventProviderConclusionObserved = "provider_conclusion_observed"
	eventGenerationPromoted         = "generation_promoted"
	eventGenerationDiscarded        = "generation_discarded"
	eventIssueObserved              = "issue_observed"
	eventClassified                 = "classified"
)

const (
	volumeWorkspace = "workspace"
	volumeToolchain = "toolchain"
	volumeData      = "data"
	volumeMemory    = "memory"
)

var allowedTraceEvents = map[string]bool{
	eventPoolReady:                  true,
	eventAssignmentObserved:         true,
	eventJobHookBlocked:             true,
	eventJobIdentityReported:        true,
	eventGenerationResolved:         true,
	eventRendezvousBound:            true,
	eventMountsReady:                true,
	eventClockChecked:               true,
	eventJobHookReleased:            true,
	eventRunnerExited:               true,
	eventSnapshotDecided:            true,
	eventSnapshotSealed:             true,
	eventProviderConclusionObserved: true,
	eventGenerationPromoted:         true,
	eventGenerationDiscarded:        true,
	eventIssueObserved:              true,
	eventClassified:                 true,
}

var concernCodes = map[string]bool{
	"clock_skew":             true,
	"lifecycle_overhead":     true,
	"platform_bug":           true,
	"slower_than_blacksmith": true,
	"snapshot_discarded":     true,
	"temporary_hardware":     true,
}

var invalidCodes = map[string]bool{
	"assignment_model_not_deployed": true,
	"evidence_incomplete":           true,
	"missing_tool":                  true,
	"preflight_failed":              true,
	"workload_misconfigured":        true,
}

var failCodes = map[string]bool{
	"durable_volume_unsound": true,
	"workload_unsupported":   true,
}

// rendezvousEvent is one append-only observation from the runner, guest, or
// host. Seq is the collector's logical order. MonotonicNS is meaningful only
// within Source and is never compared across machines.
type rendezvousEvent struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Event         string    `json:"event"`
	Seq           uint64    `json:"seq"`
	Source        string    `json:"source"`
	MonotonicNS   int64     `json:"monotonic_ns"`
	WallTime      time.Time `json:"wall_time"`

	Repo          string `json:"repo,omitempty"`
	Lane          string `json:"lane,omitempty"`
	JobID         int64  `json:"job_id,omitempty"`
	RunAttempt    int    `json:"run_attempt,omitempty"`
	RunnerName    string `json:"runner_name,omitempty"`
	VMID          string `json:"vm_id,omitempty"`
	GenerationSet string `json:"generation_set,omitempty"`
	Conclusion    string `json:"conclusion,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`

	Volumes        []volumeEvidence        `json:"volumes,omitempty"`
	Platform       *platformEvidence       `json:"platform,omitempty"`
	Clock          *clockEvidence          `json:"clock,omitempty"`
	Snapshot       *snapshotEvidence       `json:"snapshot,omitempty"`
	Issue          *issueEvidence          `json:"issue,omitempty"`
	Classification *classificationEvidence `json:"classification,omitempty"`
}

type volumeEvidence struct {
	Role            string `json:"role"`
	Dataset         string `json:"dataset"`
	Materialization string `json:"materialization"`
	SnapshotGUID    string `json:"snapshot_guid"`
	Generation      string `json:"generation"`
	DeviceSerial    string `json:"device_serial"`
}

type platformEvidence struct {
	QEMUVersion   string `json:"qemu_version"`
	KernelRelease string `json:"kernel_release"`
	OSImageID     string `json:"os_image_id"`
	MachineType   string `json:"machine_type"`
	CPUModel      string `json:"cpu_model"`
	CRIUVersion   string `json:"criu_version,omitempty"`
}

// clockEvidence brackets one guest realtime sample with host samples. The
// validator uses midpoint offset plus half the round-trip as a conservative
// skew bound; durations elsewhere come from monotonic clocks.
type clockEvidence struct {
	HostBeforeUnixNS  int64  `json:"host_before_unix_ns"`
	HostAfterUnixNS   int64  `json:"host_after_unix_ns"`
	GuestUnixNS       int64  `json:"guest_unix_ns"`
	MaxSkewNS         int64  `json:"max_skew_ns"`
	GuestSynchronized bool   `json:"guest_synchronized"`
	Clocksource       string `json:"clocksource"`
	AfterRestore      bool   `json:"after_restore"`
}

type snapshotEvidence struct {
	Policy               string   `json:"policy"`
	Decision             string   `json:"decision"`
	Reason               string   `json:"reason,omitempty"`
	TrustedRef           bool     `json:"trusted_ref"`
	RunnerExited         bool     `json:"runner_exited"`
	RunnerMemoryIncluded bool     `json:"runner_memory_included"`
	FilesystemsQuiesced  bool     `json:"filesystems_quiesced"`
	AllowedProcesses     []string `json:"allowed_processes,omitempty"`
	CapturedProcesses    []string `json:"captured_processes,omitempty"`
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
	SchemaVersion int              `json:"schema_version"`
	RunID         string           `json:"run_id"`
	Repo          string           `json:"repo,omitempty"`
	Lane          string           `json:"lane,omitempty"`
	JobID         int64            `json:"job_id,omitempty"`
	RunAttempt    int              `json:"run_attempt,omitempty"`
	RunnerName    string           `json:"runner_name,omitempty"`
	VMID          string           `json:"vm_id,omitempty"`
	GenerationSet string           `json:"generation_set,omitempty"`
	Events        int              `json:"events"`
	Outcome       benchmarkOutcome `json:"outcome"`
	TraceValid    bool             `json:"trace_valid"`
	Violations    []string         `json:"violations,omitempty"`
	Concerns      []string         `json:"concerns,omitempty"`
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
		SchemaVersion: rendezvousTraceSchema,
		Events:        len(events),
		Outcome:       outcomePass,
		TraceValid:    true,
	}
	seen := map[string]bool{}
	monotonicBySource := map[string]int64{}
	var volumes []volumeEvidence
	var resolvedVolumes []volumeEvidence
	var explicit *classificationEvidence
	var runnerExitCode *int
	var platform *platformEvidence
	var snapshotDecision string
	var providerConclusion string

	violate := func(format string, args ...any) {
		report.Violations = append(report.Violations, fmt.Sprintf(format, args...))
	}
	concern := func(format string, args ...any) {
		report.Concerns = append(report.Concerns, fmt.Sprintf(format, args...))
	}
	requireSeen := func(event rendezvousEvent, prerequisites ...string) {
		for _, prerequisite := range prerequisites {
			if !seen[prerequisite] {
				violate("event %s at seq %d requires prior %s", event.Event, event.Seq, prerequisite)
			}
		}
	}

	var previousSeq uint64
	for index, event := range events {
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
		if event.Source == "" {
			violate("event %s at seq %d has no source", event.Event, event.Seq)
		} else if event.MonotonicNS <= 0 {
			violate("event %s at seq %d has no positive monotonic_ns", event.Event, event.Seq)
		} else if previous, ok := monotonicBySource[event.Source]; ok && event.MonotonicNS < previous {
			violate("source %s monotonic_ns moved backward from %d to %d", event.Source, previous, event.MonotonicNS)
		} else {
			monotonicBySource[event.Source] = event.MonotonicNS
		}
		if event.WallTime.IsZero() {
			violate("event %s at seq %d has no wall_time", event.Event, event.Seq)
		}
		if event.RunID == "" {
			violate("event %s at seq %d has no run_id", event.Event, event.Seq)
		}
		if report.RunID == "" {
			report.RunID = event.RunID
		} else if event.RunID != report.RunID {
			violate("event %s names run %q, want %q", event.Event, event.RunID, report.RunID)
		}
		mergeTraceIdentity(report, event, violate)
		if seen[event.Event] && event.Event != eventIssueObserved {
			violate("event %s appears more than once", event.Event)
		}

		switch event.Event {
		case eventPoolReady:
			if event.RunnerName == "" || event.VMID == "" {
				violate("pool_ready requires runner_name and vm_id")
			}
			if event.Repo != "" || event.JobID != 0 || event.RunAttempt != 0 || event.GenerationSet != "" {
				violate("pool_ready VM %s already carries customer identity", event.VMID)
			}
			if len(event.Volumes) != 0 {
				violate("pool_ready VM %s already carries customer volumes", event.VMID)
			}
			validatePlatform(event.Platform, violate)
			platform = event.Platform

		case eventAssignmentObserved:
			requireSeen(event, eventPoolReady)
			if event.JobID <= 0 || event.RunAttempt <= 0 || event.RunnerName == "" {
				violate("assignment_observed requires the provider job_id, run_attempt, and actual runner_name")
			}

		case eventJobHookBlocked:
			requireSeen(event, eventAssignmentObserved)
			if event.JobID <= 0 || event.RunAttempt <= 0 || event.RunnerName == "" {
				violate("job_hook_blocked requires job_id, run_attempt, and runner_name")
			}

		case eventJobIdentityReported:
			requireSeen(event, eventJobHookBlocked)
			if event.JobID <= 0 || event.RunAttempt <= 0 || event.RunnerName == "" || event.Repo == "" {
				violate("job_identity_reported requires job_id, run_attempt, runner_name, and repo")
			}

		case eventGenerationResolved:
			requireSeen(event, eventJobIdentityReported)
			if event.JobID <= 0 || event.RunAttempt <= 0 || event.RunnerName == "" || event.Repo == "" || event.GenerationSet == "" {
				violate("generation_resolved requires job_id, run_attempt, runner_name, repo, and generation_set")
			}
			validateVolumes(event.Volumes, platform, false, violate)
			resolvedVolumes = append([]volumeEvidence(nil), event.Volumes...)

		case eventRendezvousBound:
			requireSeen(event, eventGenerationResolved)
			if event.Seq != 6 {
				violate("rendezvous_bound is logical step 6 and must have seq 6, got %d", event.Seq)
			}
			if event.JobID <= 0 || event.RunAttempt <= 0 || event.RunnerName == "" || event.VMID == "" || event.GenerationSet == "" {
				violate("rendezvous_bound requires job_id, run_attempt, runner_name, vm_id, and generation_set")
			}
			validateVolumes(event.Volumes, platform, true, violate)
			compareVolumes("rendezvous_bound", resolvedVolumes, event.Volumes, violate)
			volumes = append([]volumeEvidence(nil), event.Volumes...)

		case eventMountsReady:
			requireSeen(event, eventRendezvousBound)
			validateVolumes(event.Volumes, platform, true, violate)
			compareVolumes("mounts_ready", volumes, event.Volumes, violate)

		case eventClockChecked:
			requireSeen(event, eventMountsReady)
			validateClock(event.Clock, hasVolumeRole(volumes, volumeMemory), concern, violate)

		case eventJobHookReleased:
			requireSeen(event, eventClockChecked)

		case eventRunnerExited:
			requireSeen(event, eventJobHookReleased)
			if event.ExitCode == nil {
				violate("runner_exited requires exit_code")
			} else {
				code := *event.ExitCode
				runnerExitCode = &code
			}

		case eventSnapshotDecided:
			requireSeen(event, eventRunnerExited)
			validateSnapshotDecision(event, report.GenerationSet, runnerExitCode, violate)
			if event.Snapshot != nil {
				snapshotDecision = event.Snapshot.Decision
			}

		case eventSnapshotSealed:
			requireSeen(event, eventSnapshotDecided)
			validateSnapshotSeal(event, report.GenerationSet, volumes, violate)

		case eventProviderConclusionObserved:
			requireSeen(event, eventRunnerExited)
			if event.JobID <= 0 || event.RunAttempt <= 0 || event.Conclusion == "" {
				violate("provider_conclusion_observed requires job_id, run_attempt, and conclusion")
			} else {
				providerConclusion = event.Conclusion
			}

		case eventGenerationPromoted:
			requireSeen(event, eventSnapshotSealed, eventProviderConclusionObserved)
			if providerConclusion != "success" {
				violate("generation_promoted requires the attempt-scoped success conclusion")
			}

		case eventGenerationDiscarded:
			requireSeen(event, eventSnapshotDecided)

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
		seen[event.Event] = true
	}

	if explicit == nil || explicit.Outcome == outcomePass || explicit.Outcome == outcomeConcern {
		requiredEvents := []string{
			eventPoolReady,
			eventAssignmentObserved,
			eventJobHookBlocked,
			eventJobIdentityReported,
			eventGenerationResolved,
			eventRendezvousBound,
			eventMountsReady,
			eventClockChecked,
			eventJobHookReleased,
		}
		if !throughRelease {
			requiredEvents = append(requiredEvents,
				eventRunnerExited,
				eventSnapshotDecided,
				eventProviderConclusionObserved,
			)
		}
		for _, required := range requiredEvents {
			if !seen[required] {
				violate("complete trace is missing %s", required)
			}
		}
		if snapshotDecision == "generate" {
			if !seen[eventSnapshotSealed] {
				violate("generated snapshot candidate is missing snapshot_sealed")
			}
			if !seen[eventGenerationPromoted] && !seen[eventGenerationDiscarded] {
				violate("generated snapshot candidate was neither promoted nor discarded")
			}
			if providerConclusion == "success" && !seen[eventGenerationPromoted] {
				violate("successful generated snapshot candidate was not promoted")
			}
			if providerConclusion != "" && providerConclusion != "success" && !seen[eventGenerationDiscarded] {
				violate("unsuccessful generated snapshot candidate was not discarded")
			}
		}
	}

	if len(report.Violations) > 0 {
		report.TraceValid = false
		report.Outcome = outcomeInvalid
		return report
	}
	if explicit != nil {
		report.Outcome = explicit.Outcome
	}
	if runnerExitCode != nil && *runnerExitCode != 0 && report.Outcome != outcomeFail {
		report.Outcome = outcomeInvalid
	}
	if report.Outcome == outcomePass && len(report.Concerns) > 0 {
		report.Outcome = outcomeConcern
	}
	return report
}

func mergeTraceIdentity(report *rendezvousTraceReport, event rendezvousEvent, violate func(string, ...any)) {
	for _, field := range []struct {
		name string
		got  string
		dst  *string
	}{
		{"repo", event.Repo, &report.Repo},
		{"lane", event.Lane, &report.Lane},
		{"runner_name", event.RunnerName, &report.RunnerName},
		{"vm_id", event.VMID, &report.VMID},
		{"generation_set", event.GenerationSet, &report.GenerationSet},
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
	for _, field := range []struct {
		name  string
		value string
	}{
		{"qemu_version", platform.QEMUVersion},
		{"kernel_release", platform.KernelRelease},
		{"os_image_id", platform.OSImageID},
		{"machine_type", platform.MachineType},
		{"cpu_model", platform.CPUModel},
	} {
		if field.value == "" {
			violate("platform fingerprint has no %s", field.name)
		}
	}
}

func validateVolumes(volumes []volumeEvidence, platform *platformEvidence, requireDeviceSerial bool, violate func(string, ...any)) {
	roles := map[string]bool{}
	datasets := map[string]bool{}
	deviceSerials := map[string]bool{}
	for _, volume := range volumes {
		if roles[volume.Role] {
			violate("rendezvous contains duplicate %s volume", volume.Role)
		}
		roles[volume.Role] = true
		if volume.Role != volumeWorkspace && volume.Role != volumeToolchain && volume.Role != volumeData && volume.Role != volumeMemory {
			violate("rendezvous contains unknown volume role %q", volume.Role)
		}
		if volume.Dataset == "" {
			violate("rendezvous %s volume lacks dataset", volume.Role)
		}
		switch volume.Materialization {
		case "empty":
			if volume.Generation != "" || volume.SnapshotGUID != "" {
				violate("empty rendezvous %s volume names a source generation or snapshot_guid", volume.Role)
			}
			if volume.Role == volumeMemory {
				violate("memory volume cannot use empty materialization")
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
		if datasets[volume.Dataset] {
			violate("rendezvous contains duplicate dataset %q", volume.Dataset)
		}
		datasets[volume.Dataset] = true
		if requireDeviceSerial && volume.DeviceSerial == "" {
			violate("rendezvous %s volume lacks device_serial", volume.Role)
		}
		if volume.DeviceSerial != "" {
			if deviceSerials[volume.DeviceSerial] {
				violate("rendezvous contains duplicate device_serial %q", volume.DeviceSerial)
			}
			deviceSerials[volume.DeviceSerial] = true
		}
	}
	if !roles[volumeWorkspace] {
		violate("rendezvous has no workspace volume")
	}
	if roles[volumeMemory] && !roles[volumeWorkspace] {
		violate("memory snapshot without its workspace is not a valid rendezvous")
	}
	if roles[volumeMemory] && (platform == nil || platform.CRIUVersion == "") {
		violate("memory snapshot rendezvous has no CRIU version in the platform fingerprint")
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
		if volume.Dataset != want.Dataset ||
			volume.Materialization != want.Materialization ||
			volume.SnapshotGUID != want.SnapshotGUID ||
			volume.Generation != want.Generation {
			violate("%s %s volume does not match the resolved generation tuple", event, volume.Role)
		}
		if want.DeviceSerial != "" && volume.DeviceSerial != want.DeviceSerial {
			violate("%s %s volume device_serial %q does not match %q",
				event, volume.Role, volume.DeviceSerial, want.DeviceSerial)
		}
	}
}

func hasVolumeRole(volumes []volumeEvidence, role string) bool {
	return slices.ContainsFunc(volumes, func(volume volumeEvidence) bool {
		return volume.Role == role
	})
}

func validateClock(clock *clockEvidence, afterMemoryRestore bool, concern, violate func(string, ...any)) {
	if clock == nil {
		violate("clock_checked requires clock evidence")
		return
	}
	if clock.HostBeforeUnixNS <= 0 || clock.HostAfterUnixNS <= 0 || clock.GuestUnixNS <= 0 {
		violate("clock evidence requires positive host and guest realtime samples")
		return
	}
	if clock.HostAfterUnixNS < clock.HostBeforeUnixNS {
		violate("clock host bracket moved backward")
		return
	}
	if clock.MaxSkewNS <= 0 {
		violate("clock evidence requires a positive max_skew_ns")
	}
	if clock.Clocksource == "" {
		violate("clock evidence requires the guest clocksource")
	}
	if afterMemoryRestore && !clock.AfterRestore {
		violate("memory rendezvous requires clock evidence collected after restore")
	}
	midpoint := clock.HostBeforeUnixNS + (clock.HostAfterUnixNS-clock.HostBeforeUnixNS)/2
	uncertainty := (clock.HostAfterUnixNS - clock.HostBeforeUnixNS) / 2
	bound := abs64(clock.GuestUnixNS-midpoint) + uncertainty
	if bound > clock.MaxSkewNS {
		concern("clock_skew: conservative offset bound %s exceeds %s",
			time.Duration(bound), time.Duration(clock.MaxSkewNS))
	}
	if !clock.GuestSynchronized {
		concern("clock_skew: guest time synchronization was not healthy")
	}
}

func validateSnapshotDecision(event rendezvousEvent, generationSet string, exitCode *int, violate func(string, ...any)) {
	snapshot := event.Snapshot
	if snapshot == nil {
		violate("snapshot_decided requires snapshot evidence")
		return
	}
	if snapshot.Decision != "generate" && snapshot.Decision != "skip" {
		violate("snapshot decision %q is neither generate nor skip", snapshot.Decision)
		return
	}
	if snapshot.Decision == "skip" {
		if snapshot.Reason == "" {
			violate("skipped snapshot decision requires a reason")
		}
		return
	}
	if snapshot.Policy != "protected_main" && snapshot.Policy != "benchmark_seed" {
		violate("snapshot generation policy %q is not protected_main or benchmark_seed", snapshot.Policy)
	}
	if !snapshot.TrustedRef {
		violate("snapshot generation requires a trusted protected ref or explicit benchmark seed")
	}
	if exitCode == nil || *exitCode != 0 {
		violate("snapshot generation requires a locally successful runner exit")
	}
	if !snapshot.RunnerExited {
		violate("snapshot generation began before the Actions runner exited")
	}
	if snapshot.RunnerMemoryIncluded {
		violate("Actions runner memory must never enter a snapshot")
	}
	if event.GenerationSet == "" || event.GenerationSet != generationSet {
		violate("snapshot decision generation_set %q does not match rendezvous %q", event.GenerationSet, generationSet)
	}
	for _, process := range snapshot.CapturedProcesses {
		if !slices.Contains(snapshot.AllowedProcesses, process) {
			violate("snapshot captures process %q outside the allowlist", process)
		}
	}
}

func validateSnapshotSeal(event rendezvousEvent, generationSet string, bound []volumeEvidence, violate func(string, ...any)) {
	snapshot := event.Snapshot
	if snapshot == nil || snapshot.Decision != "generate" {
		violate("snapshot_sealed requires generate snapshot evidence")
		return
	}
	if event.GenerationSet != generationSet {
		violate("sealed generation_set %q does not match rendezvous %q", event.GenerationSet, generationSet)
	}
	if !snapshot.FilesystemsQuiesced {
		violate("snapshot sealed without quiescing its durable filesystems")
	}
	if snapshot.RunnerMemoryIncluded {
		violate("sealed snapshot includes Actions runner memory")
	}
	if len(event.Volumes) == 0 {
		violate("snapshot_sealed records no volume snapshots")
	}
	boundRoles := map[string]bool{}
	for _, volume := range bound {
		boundRoles[volume.Role] = true
	}
	sealedRoles := map[string]bool{}
	for _, volume := range event.Volumes {
		if sealedRoles[volume.Role] {
			violate("snapshot sealed duplicate %s volume", volume.Role)
		}
		sealedRoles[volume.Role] = true
		if !boundRoles[volume.Role] {
			violate("snapshot sealed unbound %s volume", volume.Role)
		}
		if volume.Dataset == "" || volume.SnapshotGUID == "" || volume.Generation == "" {
			violate("snapshot sealed %s volume without dataset, snapshot_guid, or generation", volume.Role)
		}
	}
	for _, volume := range bound {
		if !sealedRoles[volume.Role] {
			violate("snapshot did not seal bound %s volume", volume.Role)
		}
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
	fmt.Fprintf(w, "job=%d attempt=%d runner=%s vm=%s generation_set=%s events=%d\n",
		report.JobID, report.RunAttempt, report.RunnerName, report.VMID, report.GenerationSet, report.Events)
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
	fs.BoolVar(&throughRelease, "through-release", false, "validate the assignment-first rendezvous through job_hook_released without requiring post-job lifecycle evidence")
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
