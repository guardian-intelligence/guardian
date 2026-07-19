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

// Class is a runner class, e.g. postflight-4cpu-ubuntu-2404.
type Class string

// Phase is a VM's observed lifecycle phase. The driver reports phases; the
// agent never assumes a transition it did not observe.
type Phase string

const (
	// PhaseBooting: launched, not yet ready to accept an assignment.
	PhaseBooting Phase = "booting"
	// PhaseWarm: booted and idle in the pool.
	PhaseWarm Phase = "warm"
	// PhaseAssigned: the generic runner registration was delivered.
	PhaseAssigned Phase = "assigned"
	// PhaseListening: the runner is registered and carries no customer
	// volume or identity.
	PhaseListening Phase = "listening"
	// PhaseHookBlocked: GitHub assigned the runner and its start hook is
	// blocked before customer steps.
	PhaseHookBlocked Phase = "hook-blocked"
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
	Lease    string
	ExitCode int
	Identity JobIdentity
	Clock    ClockSample
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

// Preparation turns a warm VM into a generic GitHub listener.
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
	// filesystem a later Quiesce syncs and unmounts.
	WorkspaceMountpoint string
	// Env is written into the job environment by the still-blocked hook.
	Env map[string]string
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
	// Prepare registers the generic runner without customer volumes.
	Prepare(ctx context.Context, id ID, preparation Preparation) error
	// Rendezvous hot-attaches the assigned job's workspace and releases it.
	Rendezvous(ctx context.Context, id ID, rendezvous Rendezvous) error
	// Status reports one VM. Phase is PhaseGone for unknown IDs.
	Status(ctx context.Context, id ID) (Status, error)
	// List reports every VM the driver knows on this host.
	List(ctx context.Context) ([]Status, error)
	// Quiesce asks the guest to sync and unmount its workspace ahead of the
	// host-side seal snapshot. Nil only when the guest confirmed; any other
	// outcome skips the seal — ambiguity never promotes.
	Quiesce(ctx context.Context, id ID) error
	// Destroy tears a VM down (destroy-and-refill; never reuse). Idempotent:
	// destroying an absent VM succeeds.
	Destroy(ctx context.Context, id ID) error
}
