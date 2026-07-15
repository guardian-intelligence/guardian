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
	// PhaseAssigned: workspace attached and runner env delivered; the guest
	// is bringing the runner up.
	PhaseAssigned Phase = "assigned"
	// PhaseReady: the runner inside is registered and listening for its job.
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
	Phase Phase
	// Lease is the lease this VM is assigned to, empty for pool VMs. The
	// driver persists it with the VM (the real driver keeps it in the
	// per-VM state dir) so a restarted hostd can rebind instead of leaking.
	Lease    string
	ExitCode int
}

// Assignment carries everything a warm VM needs to become a job runner.
type Assignment struct {
	// Lease is the lease this assignment serves; surfaced back via Status.
	Lease string
	// WorkspaceDevice is the host block device to hot-attach; it appears in
	// the guest under a stable serial so the mount is deterministic.
	WorkspaceDevice string
	// JITConfig is the encoded single-use runner registration blob.
	JITConfig string
	// Env is the runner environment (POSTFLIGHT_* checkout variables).
	Env map[string]string
}

// ErrNotFound: the named VM does not exist.
var ErrNotFound = errors.New("vm: not found")

// Driver is the hypervisor surface hostd needs. Launch and Assign start
// asynchronous work; the agent advances on observed Status changes, never on
// call returns. All methods are safe to repeat: Launch with an existing ID
// and Assign on an already-assigned VM are no-ops.
type Driver interface {
	// Launch boots a warm VM of a class under the given ID.
	Launch(ctx context.Context, id ID, class Class) error
	// Assign hot-attaches the workspace and delivers the runner assignment
	// to a warm VM.
	Assign(ctx context.Context, id ID, assignment Assignment) error
	// Status reports one VM. Phase is PhaseGone for unknown IDs.
	Status(ctx context.Context, id ID) (Status, error)
	// List reports every VM the driver knows on this host.
	List(ctx context.Context) ([]Status, error)
	// Destroy tears a VM down (destroy-and-refill; never reuse). Idempotent:
	// destroying an absent VM succeeds.
	Destroy(ctx context.Context, id ID) error
}
