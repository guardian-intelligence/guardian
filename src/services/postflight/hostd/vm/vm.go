// Package vm defines the sandbox-VM surface hostd drives: warm-pool
// launches, workspace hot-attach, runner assignment, and destruction. The
// Driver interface has two implementations: QEMU (real QEMU/KVM processes,
// shaped by the tracer-proven flow — pre-booted warm VM, hot attach by
// stable device serial, destroy-and-refill, never reuse) and Fake
// (in-memory, for the agent core's tests and the sim harness). Everything
// above the interface is hermetically testable; the QEMU implementation is
// verified by the conformance suite on a real host.
package vm

import (
	"context"
	"errors"
)

// ID names one VM instance on this host.
type ID string

// Class is a runner class, e.g. postflight-4-ubuntu-24.04-github-confidential.
type Class string

// Phase is a VM's observed lifecycle phase. The driver reports phases; the
// agent never assumes a transition it did not observe.
type Phase string

const (
	// PhaseBooting: launched, not yet ready to accept an assignment.
	PhaseBooting Phase = "booting"
	// PhaseWarm: booted and idle in the pool.
	PhaseWarm Phase = "warm"
	// PhaseAssigned: listener configuration was delivered to the generic VM.
	PhaseAssigned Phase = "assigned"
	// PhaseBound: the exact generation selected by the local assignment is
	// mounted and restored while Runner.Worker remains blocked.
	PhaseBound Phase = "bound"
	// PhaseListening: the runner is registered and carries no customer
	// volume or identity.
	PhaseListening Phase = "listening"
	// PhaseJobAssigned: GitHub assigned a job to the local listener, which is
	// synchronously blocked before Runner.Worker creation.
	PhaseJobAssigned Phase = "job-assigned"
	// PhaseHookBlocked: the job-start hook validated the assigned identity and
	// is blocked before customer steps.
	PhaseHookBlocked Phase = "hook-blocked"
	// PhaseWorkerReady: the restored capsule has been selected and the
	// Runner.Worker trampoline was released to enter it.
	PhaseWorkerReady Phase = "worker-ready"
	// PhaseReady: the exact generation tuple is mounted and the hook released.
	PhaseReady Phase = "ready"
	// PhaseExited: the runner finished (or died); ExitCode is meaningful.
	PhaseExited Phase = "exited"
	// PhaseGone: the VM no longer exists.
	PhaseGone Phase = "gone"
)

// Status is one VM's observed state.
type Status struct {
	ID    ID
	Class Class
	// Image identifies the immutable golden snapshot this VM's root cloned.
	// The pool governor uses it to roll idle listeners after an image change.
	Image string
	Phase Phase
	// Lease is the lease this VM is assigned to, empty for pool VMs. The
	// driver persists it with the VM (the real driver keeps it in the
	// per-VM state dir) so a restarted hostd can rebind instead of leaking.
	Lease                 string
	ExitCode              int
	FailureReason         string
	CustomerStepsReleased bool
	Identity              JobIdentity
	Assignment            Assignment
	Clock                 ClockSample
	Timing                []TimingPoint
}

type Assignment struct {
	RequestID      string
	JobID          string
	RunnerName     string
	JobDisplayName string
	Identity       JobIdentity
}

type TimingPoint struct {
	Event       string
	Source      string
	BootID      string
	Sequence    uint64
	MonotonicNS int64
	UnixNS      int64
}

type JobIdentity struct {
	RunID       string
	RunAttempt  int
	RunnerName  string
	Repository  string
	WorkflowJob string
}

type ClockSample struct {
	UnixNS       int64
	Synchronized bool
	Clocksource  string
	AfterRestore bool
}

// Preparation turns a generic warm VM into a fresh GitHub listener without
// selecting or attaching tenant state.
type Preparation struct {
	Lease     string
	JITConfig string
	Env       map[string]string
}

// Rendezvous carries everything bound only after GitHub's actual assignment.
type Rendezvous struct {
	Lease string
	// WorkspaceDevice is the host block device to hot-attach; it appears in
	// the guest under a stable serial so the mount is deterministic.
	WorkspaceDevice string
	// WorkspaceMountpoint is where the guest mounts the workspace, and the
	// filesystem a later Quiesce checkpoints and flushes.
	WorkspaceMountpoint string
	// ProcessDevice is the paired encrypted CRIU image zvol.
	ProcessDevice string
	// CheckpointDigest selects a restorable process generation. Empty is a
	// deliberate workspace-only cold fallback.
	CheckpointDigest string
	// CheckpointVersion is the exact CRIU format identity recorded with the
	// generation. It is verified inside the guest before CRIU reads images.
	CheckpointVersion string
}

type Authorization struct {
	Lease     string
	RequestID string
	Identity  JobIdentity
	// Env is written into the job environment by the still-blocked hook.
	Env map[string]string
}

type CheckpointArtifact struct {
	Digest  string
	Version string
	Timing  []TimingPoint
}

// ErrNotFound: the named VM does not exist.
var ErrNotFound = errors.New("vm: not found")

// Driver is the hypervisor surface hostd needs. Launch and Assign start
// asynchronous work; the agent advances on observed Status changes, never on
// call returns. All methods are safe to repeat: relaunching a live ID with
// its own class and re-assigning a VM's own lease converge to no-ops, while
// a class or lease mismatch is an error. A Launch that finds its ID's
// process dead collects the leftovers and boots fresh.
type Driver interface {
	// Launch boots a warm VM of a class under the given ID.
	Launch(ctx context.Context, id ID, class Class) error
	// Rendezvous binds and restores the exact generation selected after the
	// local listener received GitHub's assignment.
	Rendezvous(ctx context.Context, id ID, rendezvous Rendezvous) error
	// Prepare starts a fresh listener on the generic warm VM.
	Prepare(ctx context.Context, id ID, preparation Preparation) error
	// Authorize releases the blocked runner after exact assignment proof.
	Authorize(ctx context.Context, id ID, authorization Authorization) error
	// Status reports one VM. Phase is PhaseGone for unknown IDs.
	Status(ctx context.Context, id ID) (Status, error)
	// List reports every VM the driver knows on this host.
	List(ctx context.Context) ([]Status, error)
	// Quiesce asks the guest to checkpoint and flush the paired generation.
	// The VM must be destroyed successfully before either volume is sealed;
	// any ambiguous outcome skips the seal.
	Quiesce(ctx context.Context, id ID) (CheckpointArtifact, error)
	// Destroy tears a VM down (destroy-and-refill; never reuse). Idempotent:
	// destroying an absent VM succeeds.
	Destroy(ctx context.Context, id ID) error
}

// UpdateSource reports substrate state changes as edge-triggered hints. The
// assignment hot path re-observes only the named VM through Status; periodic
// List remains the level-triggered repair path for omitted or coalesced hints.
type UpdateSource interface {
	Updates() <-chan ID
}
