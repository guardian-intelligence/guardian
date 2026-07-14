package sim

import (
	"fmt"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// WorldState is everything an invariant may quantify over: the agent's own
// view (lease snapshots), the substrate's ground truth (fake driver state),
// and the scripted control plane's ledger (what was desired, what was
// ordered reaped).
type WorldState struct {
	Now         time.Time
	Leases      []agent.LeaseSnapshot
	VMs         []vm.Status
	Generations []zvol.GenerationSnapshot
	Workspaces  []zvol.WorkspaceVolume
	Slots       map[vm.Class]int
	SealedEver  map[string]bool
	Reaped      map[string]bool
	Desired     map[string]agent.DesiredLease
	// Resolves reports whether the checkout resolver currently accepts an
	// (execution, attempt) pair.
	Resolves func(executionID, attemptID string) bool
}

// Invariant is one property that must hold after every step. Check returns
// "" when the property holds, or a description of the violation.
type Invariant struct {
	Name  string
	Check func(WorldState) string
}

// Invariants is the full set, evaluated after every Sync and Tick of every
// scenario. Each entry has a matching vacuity proof in vacuity_test.go.
var Invariants = []Invariant{
	{Name: "vm-per-lease", Check: checkVMPerLease},
	{Name: "slot-bounds", Check: checkSlotBounds},
	{Name: "sealed-survives", Check: checkSealedSurvives},
	{Name: "deadline-release", Check: checkDeadlineRelease},
	{Name: "terminal-never-resolves", Check: checkTerminalNeverResolves},
	{Name: "failed-holds-no-vm", Check: checkFailedHoldsNoVM},
}

// vm-per-lease: no two leases claim the same VM, and every VM that reports
// a lease is a lease the agent actually tracks in a VM-holding state.
func checkVMPerLease(state WorldState) string {
	owners := map[string]string{}
	for _, lease := range state.Leases {
		if lease.VMID == "" {
			continue
		}
		if other, taken := owners[lease.VMID]; taken {
			return fmt.Sprintf("vm %s claimed by both %s and %s", lease.VMID, other, lease.LeaseID)
		}
		owners[lease.VMID] = lease.LeaseID
	}
	return ""
}

// slot-bounds: a class never has more VMs than its slot total.
func checkSlotBounds(state WorldState) string {
	counts := map[vm.Class]int{}
	for _, status := range state.VMs {
		counts[status.Class]++
	}
	for class, count := range counts {
		if total, ok := state.Slots[class]; ok && count > total {
			return fmt.Sprintf("class %s has %d vms over %d slots", class, count, total)
		}
	}
	return ""
}

// sealed-survives: a generation that was ever resident still exists unless
// the control plane ordered it reaped. This is the node-local-volumes
// safety property — hostd never destroys the only copy on its own.
func checkSealedSurvives(state WorldState) string {
	resident := map[string]bool{}
	for _, generation := range state.Generations {
		resident[string(generation.Generation)] = true
	}
	for generation := range state.SealedEver {
		if state.Reaped[generation] {
			continue
		}
		if !resident[generation] {
			return fmt.Sprintf("generation %s vanished without a reap verb", generation)
		}
	}
	return ""
}

// deadline-release: no lease sits in a bounded state past its deadline by
// more than one tick's worth of slack.
func checkDeadlineRelease(state WorldState) string {
	const slack = time.Second
	for _, lease := range state.Leases {
		deadline, bounded := agent.StateDeadline(lease.State)
		if !bounded {
			continue
		}
		if overdue := state.Now.Sub(lease.Since) - deadline; overdue > slack {
			return fmt.Sprintf("lease %s stuck in %s for %s past its deadline", lease.LeaseID, lease.State, overdue)
		}
	}
	return ""
}

// terminal-never-resolves: checkout-token validity ≡ lease liveness. A
// terminal lease must not resolve, no matter what token material exists.
func checkTerminalNeverResolves(state WorldState) string {
	for _, lease := range state.Leases {
		if !agent.Terminal(lease.State) || lease.ExecutionID == "" {
			continue
		}
		if state.Resolves(lease.ExecutionID, lease.AttemptID) {
			return fmt.Sprintf("terminal lease %s still resolves for checkout", lease.LeaseID)
		}
	}
	return ""
}

// failed-holds-no-vm: terminal leases hold no VM — failure and cancellation
// always release the slot.
func checkFailedHoldsNoVM(state WorldState) string {
	for _, lease := range state.Leases {
		if agent.Terminal(lease.State) && lease.VMID != "" {
			return fmt.Sprintf("terminal lease %s still holds vm %s", lease.LeaseID, lease.VMID)
		}
	}
	return ""
}
