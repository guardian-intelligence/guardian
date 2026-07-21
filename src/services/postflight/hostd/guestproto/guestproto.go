// Package guestproto is the wire contract between hostd and guestd: JSON
// lines over the per-VM vsock channel. guestd reports hello (booted and
// idle), the runner lifecycle, and quiesce outcomes up; hostd delivers the
// runner assignment and the pre-seal quiesce down. Both ends frame through
// Encoder/Decoder so the message vocabulary and size bounds live in exactly
// one place.
package guestproto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Version is the protocol revision a Hello announces.
const Version = 6

// WorkspaceReadyMarker is the file guestd drops at a converged workspace
// mountpoint's root once every declared mount is in place. It is the shared
// host↔guest↔checkout-action contract: guestd writes it, the host advertises
// its path as POSTFLIGHT_WORKSPACE_READY_FILE, and the checkout action
// refuses to run on a workspace without it.
const WorkspaceReadyMarker = ".postflight-workspace"

// VsockPort is where guestd listens inside every VM; the host dials the
// VM's CID on this port.
const VsockPort = 1

// DiskByIDPrefix is where udev publishes a QEMU scsi-hd disk inside the
// guest: this prefix plus the device's serial= attribute. The vendor and
// product halves are QEMU's fixed scsi-hd inquiry strings, so the link only
// moves if the host's device model changes with it.
const DiskByIDPrefix = "/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_"

// MaxMessageBytes bounds one encoded message line. The guest is untrusted:
// a reader must never buffer an attacker-chosen amount.
const MaxMessageBytes = 1 << 20

// Kind discriminates the message union.
type Kind string

const (
	// KindHello (guest → host): guestd is up and idle.
	KindHello Kind = "hello"
	// KindPrepare (host → guest): register a listener on a generic warm VM
	// without attaching tenant state.
	KindPrepare Kind = "prepare"
	// KindRendezvous (host → guest): mount and restore the generation selected
	// by the local assignment before Runner.Worker is released.
	KindRendezvous Kind = "rendezvous"
	// KindAssignment (guest → host): the already-connected GitHub listener
	// received a job and is blocked before it spawns Runner.Worker.
	KindAssignment Kind = "assignment"
	// KindAuthorize (host → guest): the local assignment matches a staged
	// execution; release Runner.Worker into the restored capsule.
	KindAuthorize Kind = "authorize"
	// KindRunnerStatus (guest → host): the runner lifecycle advanced.
	KindRunnerStatus Kind = "runner-status"
	// KindQuiesce (host → guest): checkpoint and flush the generation
	// ahead of the host-side seal snapshot.
	KindQuiesce Kind = "quiesce"
	// KindQuiesced (guest → host): the generation is checkpointed and
	// flushed; the host must destroy QEMU before sealing it.
	KindQuiesced Kind = "quiesced"
	// KindQuiesceFailed (guest → host): the workspace could not be
	// quiesced; Reason says why.
	KindQuiesceFailed Kind = "quiesce-failed"
)

// Message is one frame on the channel: a kind and exactly the matching
// payload.
type Message struct {
	Kind          Kind           `json:"kind"`
	Hello         *Hello         `json:"hello,omitempty"`
	Prepare       *Prepare       `json:"prepare,omitempty"`
	Rendezvous    *Rendezvous    `json:"rendezvous,omitempty"`
	Assignment    *Assignment    `json:"assignment,omitempty"`
	Authorize     *Authorize     `json:"authorize,omitempty"`
	RunnerStatus  *RunnerStatus  `json:"runner_status,omitempty"`
	Quiesce       *Quiesce       `json:"quiesce,omitempty"`
	Quiesced      *Quiesced      `json:"quiesced,omitempty"`
	QuiesceFailed *QuiesceFailed `json:"quiesce_failed,omitempty"`
}

// Hello announces a booted guest.
type Hello struct {
	Version int `json:"version"`
}

// Prepare carries a single-use runner registration into an otherwise empty
// warm VM. The listener connects before any customer generation is attached.
type Prepare struct {
	// Lease names the listener. It is deliberately opaque to the guest.
	Lease string `json:"lease"`
	// JITConfig is the encoded single-use runner registration blob. It
	// exists only in guest RAM and the runner's process environment, never
	// on any disk.
	JITConfig string `json:"jit_config"`
	// Env contains only pool/listener configuration. Customer-specific
	// values arrive in Authorize after GitHub's assignment is observed.
	Env map[string]string `json:"env,omitempty"`
}

// Assignment is emitted at the earliest authoritative local boundary: after
// Runner.Listener has received the encrypted GitHub job message and before it
// asks JobDispatcher to create Runner.Worker. RequestID is GitHub's opaque
// runner request identifier; it contains no job credential.
type Assignment struct {
	RequestID      string        `json:"request_id"`
	JobID          string        `json:"job_id"`
	RunnerName     string        `json:"runner_name"`
	JobDisplayName string        `json:"job_display_name"`
	Identity       *JobIdentity  `json:"identity"`
	Timing         []TimingPoint `json:"timing,omitempty"`
}

// Rendezvous carries the generation selected for the job that the local
// listener actually received. It restores the process capsule before the
// blocked listener is allowed to spawn Runner.Worker.
type Rendezvous struct {
	Lease      string             `json:"lease"`
	Mounts     []Mount            `json:"mounts"`
	Checkpoint *CheckpointRestore `json:"checkpoint,omitempty"`
}

type Authorize struct {
	Lease     string            `json:"lease"`
	RequestID string            `json:"request_id"`
	Identity  *JobIdentity      `json:"identity,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// TimingPoint preserves the originating process's high-resolution clock.
// MonotonicNS is CLOCK_BOOTTIME and is compared only within Source/BootID;
// UnixNS exists solely for cross-machine clock brackets and log correlation.
type TimingPoint struct {
	Event       string `json:"event"`
	Source      string `json:"source"`
	BootID      string `json:"boot_id"`
	Sequence    uint64 `json:"sequence"`
	MonotonicNS int64  `json:"monotonic_ns"`
	UnixNS      int64  `json:"unix_ns"`
}

// CheckpointRestore selects the authenticated process artifact already on
// the encrypted process volume. Empty means workspace-only cold fallback.
type CheckpointRestore struct {
	ImagesDir       string `json:"images_dir"`
	ExpectedDigest  string `json:"expected_digest"`
	ExternalMountAt string `json:"external_mount_at"`
}

// Mount is one hot-attached disk the guest converges before any customer
// step runs.
type Mount struct {
	// Serial is the QEMU scsi-hd serial=; the guest locates the device via
	// /dev/disk/by-id, never by probe order.
	Serial string `json:"serial"`
	// Filesystem to create when the device is blank (a scope's first
	// generation arrives as an empty zvol).
	Filesystem string `json:"filesystem"`
	// Mountpoint is the absolute path to mount at.
	Mountpoint string `json:"mountpoint"`
	// Options for the mount. discard must be present — TRIM has to pass
	// through to the sparse zvol — and the guest enforces it.
	Options []string `json:"options,omitempty"`
}

// Quiesce asks the guest to prove the selected volumes are mounted,
// checkpoint the process capsule, and flush the filesystems. The host then
// destroys QEMU before it snapshots either zvol.
type Quiesce struct {
	Mountpoints []string        `json:"mountpoints"`
	Checkpoint  *CheckpointDump `json:"checkpoint,omitempty"`
}

type CheckpointDump struct {
	ImagesDir       string `json:"images_dir"`
	ExternalMountAt string `json:"external_mount_at"`
}

// Quiesced acknowledges a completed quiesce.
type Quiesced struct {
	Checkpoint *CheckpointArtifact `json:"checkpoint,omitempty"`
	Timing     []TimingPoint       `json:"timing,omitempty"`
}

type CheckpointArtifact struct {
	Digest  string `json:"digest"`
	Version string `json:"version"`
}

// QuiesceFailed reports a quiesce that could not complete; the host skips
// the seal (ambiguity never promotes) and destroys the VM.
type QuiesceFailed struct {
	Reason string `json:"reason"`
}

// RunnerState is the runner lifecycle as guestd observes it.
type RunnerState string

const (
	// RunnerRegistered: the runner registered and is listening for its job.
	RunnerRegistered RunnerState = "registered"
	// RunnerHookBlocked: GitHub assigned a job and its synchronous start
	// hook is performing the defense-in-depth identity check.
	RunnerHookBlocked RunnerState = "hook-blocked"
	// RunnerMountsReady: every declared device is mounted, the process
	// generation is restored, and its clock sample was taken. Runner.Worker
	// remains blocked in the listener gate.
	RunnerMountsReady RunnerState = "mounts-ready"
	// RunnerWorkerReady: the generation is restored and the outer listener
	// may spawn Runner.Worker inside the capsule.
	RunnerWorkerReady RunnerState = "worker-ready"
	// RunnerReleased: the job-start hook validated the actual workflow
	// identity and customer steps may run.
	RunnerReleased RunnerState = "released"
	// RunnerExited: the runner finished; ExitCode is meaningful.
	RunnerExited RunnerState = "exited"
)

// RunnerStatus reports the runner lifecycle up to the host.
type RunnerStatus struct {
	State    RunnerState   `json:"state"`
	ExitCode int           `json:"exit_code,omitempty"`
	Identity *JobIdentity  `json:"identity,omitempty"`
	Clock    *ClockSample  `json:"clock,omitempty"`
	Timing   []TimingPoint `json:"timing,omitempty"`
}

// JobIdentity is the identity GitHub exposes to the synchronous runner hook.
// GitHub does not expose the numeric provider job id there; the control plane
// joins this tuple to its independently observed runner assignment.
type JobIdentity struct {
	RunID       string `json:"run_id"`
	RunAttempt  int    `json:"run_attempt"`
	RunnerName  string `json:"runner_name"`
	Repository  string `json:"repository"`
	WorkflowJob string `json:"workflow_job"`
}

// ClockSample is taken after mounts and process restore and before
// Runner.Worker is released. Host samples bracket it outside the VM.
type ClockSample struct {
	UnixNS       int64  `json:"unix_ns"`
	Synchronized bool   `json:"synchronized"`
	Clocksource  string `json:"clocksource"`
	AfterRestore bool   `json:"after_restore"`
}

// Validate rejects frames whose payload does not match their kind, so a
// malformed peer fails at the seam instead of deep in a consumer.
func (m Message) Validate() error {
	payloads := 0
	for _, present := range []bool{
		m.Hello != nil, m.Prepare != nil, m.Rendezvous != nil, m.Assignment != nil, m.Authorize != nil, m.RunnerStatus != nil,
		m.Quiesce != nil, m.Quiesced != nil, m.QuiesceFailed != nil,
	} {
		if present {
			payloads++
		}
	}
	var matched bool
	switch m.Kind {
	case KindHello:
		matched = m.Hello != nil
	case KindPrepare:
		matched = m.Prepare != nil
	case KindRendezvous:
		matched = m.Rendezvous != nil
	case KindAssignment:
		matched = m.Assignment != nil
	case KindAuthorize:
		matched = m.Authorize != nil
	case KindRunnerStatus:
		matched = m.RunnerStatus != nil
	case KindQuiesce:
		matched = m.Quiesce != nil
	case KindQuiesced:
		matched = m.Quiesced != nil
	case KindQuiesceFailed:
		matched = m.QuiesceFailed != nil
	default:
		return fmt.Errorf("guestproto: unknown kind %q", m.Kind)
	}
	if !matched || payloads != 1 {
		return fmt.Errorf("guestproto: %s frame with mismatched payload", m.Kind)
	}
	return nil
}

// Encoder writes messages as JSON lines.
type Encoder struct {
	w io.Writer
}

// NewEncoder wraps a transport writer.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// Write frames one message.
func (e *Encoder) Write(m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("guestproto: encoding %s: %w", m.Kind, err)
	}
	if len(encoded) > MaxMessageBytes {
		return fmt.Errorf("guestproto: %s frame is %d bytes, limit %d", m.Kind, len(encoded), MaxMessageBytes)
	}
	_, err = e.w.Write(append(encoded, '\n'))
	return err
}

// Decoder reads messages from a stream of JSON lines.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder wraps a transport reader.
func NewDecoder(r io.Reader) *Decoder {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes)
	return &Decoder{scanner: scanner}
}

// Read returns the next message, io.EOF at a clean end of stream.
func (d *Decoder) Read() (Message, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return Message{}, fmt.Errorf("guestproto: reading frame: %w", err)
		}
		return Message{}, io.EOF
	}
	var m Message
	if err := json.Unmarshal(d.scanner.Bytes(), &m); err != nil {
		return Message{}, fmt.Errorf("guestproto: decoding frame: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}
