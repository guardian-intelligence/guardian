package vm

import (
	"context"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
)

// Guest is the guestd seam: listener preparation, rendezvous, and guest-phase
// observation ride the per-VM vsock channel, speaking the guestproto contract. The
// driver never trusts the guest for anything but its own phase — warm and
// ready exist only in the guest's vocabulary, so this is where they come
// from.
type Guest interface {
	// Prepare starts the generic listener without customer volumes.
	Prepare(ctx context.Context, id ID, cid uint32, prepare guestproto.Prepare) error
	// Rendezvous mounts the assigned job's generation tuple and releases its
	// checkpoint supervisor before the fresh runner is launched.
	Rendezvous(ctx context.Context, id ID, cid uint32, rendezvous guestproto.Rendezvous) error
	// Authorize releases the blocked job only after its observed identity
	// matches the listener lease.
	Authorize(ctx context.Context, id ID, cid uint32, authorize guestproto.Authorize) error
	// Observe reports what guestd has said about a VM. A guest that has said
	// nothing (still booting, channel not up) is the zero observation, not
	// an error.
	Observe(ctx context.Context, id ID, cid uint32) (GuestObservation, error)
	// Quiesce asks guestd to checkpoint and flush the complete generation.
	// The caller must destroy QEMU before sealing the volumes. Nil only on a
	// quiesced reply.
	Quiesce(ctx context.Context, id ID, cid uint32, request guestproto.Quiesce) (guestproto.Quiesced, error)
}

// GuestObservation is the guest-reported slice of a VM's state.
type GuestObservation struct {
	// Hello: guestd announced itself; the VM is warm.
	Hello bool
	// RunnerRegistered: the runner registered with GitHub and is listening.
	RunnerRegistered bool
	// Assignment is the exact job observed by Runner.Listener before it
	// creates Runner.Worker.
	Assignment *guestproto.Assignment
	// HookBlocked: the synchronous job-start hook reported identity.
	HookBlocked bool
	Identity    guestproto.JobIdentity
	// MountsReady follows local assignment and generation restore. Released
	// follows the defense-in-depth hook authorization. Clock is meaningful
	// once MountsReady is true.
	MountsReady bool
	WorkerReady bool
	Released    bool
	Clock       guestproto.ClockSample
	Timing      []guestproto.TimingPoint
	// RunnerExited: the runner finished; ExitCode is meaningful.
	RunnerExited bool
	ExitCode     int
}
