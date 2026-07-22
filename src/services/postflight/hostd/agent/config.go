package agent

import (
	"fmt"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
)

// Config is the agent's static shape. Everything dynamic (members, assignments, pool
// targets, reap verbs) arrives via sync; config is only what the host
// itself is.
type Config struct {
	// HostID is this host's identity with the control plane.
	HostID string
	// ControlPlaneOrigin is the sync endpoint's origin, e.g.
	// https://guardianintelligence.org.
	ControlPlaneOrigin string
	// Slots is the fixed per-class capacity provisioned on this host.
	// Warm VM ≡ slot; the pool governor and scheduler both operate within
	// these totals.
	Slots map[vm.Class]int
	// Images maps each class to its immutable golden snapshot. Idle VMs from
	// another image are destroyed and refilled before they can join a pool.
	Images map[vm.Class]string
	// SyncInterval is the default exchange cadence when the control plane
	// does not suggest one.
	SyncInterval time.Duration

	// CheckoutGuestOrigin is the checkout endpoint's origin as guests reach
	// it (the bridge address), injected into the runner environment.
	CheckoutGuestOrigin string
	// CheckoutPath is the checkout endpoint's path prefix.
	CheckoutPath string

	Platform PlatformFingerprint
}

type PlatformFingerprint struct {
	QEMUVersion   string
	KernelRelease string
	OSImageID     string
	MachineType   string
	CPUModel      string
	CRIUVersion   string
}

const defaultCheckoutPath = "/internal/sandbox/v1/github-checkout"

func (c *Config) validate() error {
	if c.HostID == "" {
		return fmt.Errorf("agent: HostID is required")
	}
	if c.ControlPlaneOrigin == "" {
		return fmt.Errorf("agent: ControlPlaneOrigin is required")
	}
	if len(c.Slots) == 0 {
		return fmt.Errorf("agent: at least one slot class is required")
	}
	for class, total := range c.Slots {
		if total <= 0 {
			return fmt.Errorf("agent: class %s has non-positive slots", class)
		}
	}
	if c.CheckoutGuestOrigin == "" {
		return fmt.Errorf("agent: CheckoutGuestOrigin is required")
	}
	if c.CheckoutPath == "" {
		c.CheckoutPath = defaultCheckoutPath
	}
	if c.SyncInterval <= 0 {
		c.SyncInterval = 2 * time.Second
	}
	return nil
}
