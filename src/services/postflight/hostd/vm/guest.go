package vm

import (
	"context"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
)

// Guest is the guestd seam: assignment delivery and guest-phase observation
// ride the per-VM vsock channel, speaking the guestproto contract. The
// driver never trusts the guest for anything but its own phase — warm and
// ready exist only in the guest's vocabulary, so this is where they come
// from.
type Guest interface {
	// Deliver hands the assignment to guestd. Idempotent per lease: guestd
	// deduplicates redelivery.
	Deliver(ctx context.Context, id ID, cid uint32, assignment guestproto.Assignment) error
	// Observe reports what guestd has said about a VM. A guest that has said
	// nothing (still booting, channel not up) is the zero observation, not
	// an error.
	Observe(ctx context.Context, id ID, cid uint32) (GuestObservation, error)
	// Quiesce asks guestd to sync and unmount the workspace so the host can
	// snapshot the zvol while the VM is still alive. Nil only on a quiesced
	// reply.
	Quiesce(ctx context.Context, id ID, cid uint32, mountpoint string) error
}

// GuestObservation is the guest-reported slice of a VM's state.
type GuestObservation struct {
	// Hello: guestd announced itself; the VM is warm.
	Hello bool
	// RunnerRegistered: the runner registered with GitHub and is listening.
	RunnerRegistered bool
	// RunnerExited: the runner finished; ExitCode is meaningful.
	RunnerExited bool
	ExitCode     int
}
