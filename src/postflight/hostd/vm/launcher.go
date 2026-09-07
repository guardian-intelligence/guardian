package vm

import (
	"context"
	"os"
)

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

// A VMM must not inherit hostd's control-plane credentials. Dropping its UID
// does not erase its environment, and a systemd scope preserves the caller's
// environment rather than constructing the clean environment of a service.
func qemuEnvironment(userManager bool) []string {
	environment := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	if userManager {
		// Only the explicit, unprivileged conformance mode needs a user bus.
		for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
			if value, ok := os.LookupEnv(key); ok {
				environment = append(environment, key+"="+value)
			}
		}
	}
	return environment
}
