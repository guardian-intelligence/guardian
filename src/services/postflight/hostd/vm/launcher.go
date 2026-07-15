package vm

import "context"

// Launcher runs a VM's QEMU process with a lifetime independent of hostd: a
// hostd restart must never kill VMs. The seam is deliberately narrow — start
// this argv, report whether it still runs, kill it — so the driver's
// lifecycle logic is identical whether the process is a direct child
// (conformance) or a pod on the host's single-node cluster (production).
//
// argv identifies the process on Alive and Kill: an implementation must
// never report a stranger as alive or kill one (pid reuse, pod name
// collision).
type Launcher interface {
	// Start launches argv detached from the caller's lifetime. The driver
	// only calls it for a VM it does not observe running.
	Start(ctx context.Context, id ID, stateDir string, argv []string) error
	// Alive reports whether the launched process still exists.
	Alive(ctx context.Context, id ID, stateDir string, argv []string) (bool, error)
	// Kill hard-stops the process and waits for it to be gone. Idempotent:
	// killing an absent process succeeds.
	Kill(ctx context.Context, id ID, stateDir string, argv []string) error
}
