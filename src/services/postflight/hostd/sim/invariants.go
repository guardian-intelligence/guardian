package sim

import (
	"fmt"
	"strings"
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
	// Named is every lease ID the control plane put in the last sync,
	// whether or not the agent accepted the spec. A named lease's live
	// resources must survive: rejection is not withdrawal.
	Named map[string]bool
	// HadResource records whether a lease ever had a VM or workspace, so the
	// invariant distinguishes "not started yet" from "collected".
	HadResource map[string]bool
	// Resolves reports whether the checkout resolver currently accepts an
	// (execution, attempt) pair.
	Resolves func(executionID, attemptID string) bool
	// Synced reports whether the agent has completed a sync exchange in its
	// current life; PostTick reports whether this state was observed after a
	// completed convergence pass (as opposed to right after a sync). Orphan
	// cleanliness only holds at the tick boundaries of a synced agent —
	// transient orphans are legal until the next pass collects them.
	Synced   bool
	PostTick bool
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
	{Name: "orphan-vms-collected", Check: checkOrphanVMsCollected},
	{Name: "slot-bounds", Check: checkSlotBounds},
	{Name: "sealed-survives", Check: checkSealedSurvives},
	{Name: "deadline-release", Check: checkDeadlineRelease},
	{Name: "terminal-never-resolves", Check: checkTerminalNeverResolves},
	{Name: "failed-holds-no-vm", Check: checkFailedHoldsNoVM},
	{Name: "desired-not-collected", Check: checkDesiredNotCollected},
}

// vm-per-lease: no two leases claim the same VM.
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

// orphan-vms-collected: at the tick boundary of a synced agent, every VM
// that reports a lease binding names a lease the agent tracks in a
// non-terminal state. Crash leftovers and pre-sync restarts legally leave
// bound VMs the agent does not track yet — but the same pass that syncs
// them must collect them, so the property holds whenever Synced && PostTick.
func checkOrphanVMsCollected(state WorldState) string {
	if !state.Synced || !state.PostTick {
		return ""
	}
	tracked := map[string]agent.LeaseSnapshot{}
	for _, lease := range state.Leases {
		tracked[lease.LeaseID] = lease
	}
	for _, status := range state.VMs {
		if status.Lease == "" {
			continue
		}
		lease, ok := tracked[status.Lease]
		if !ok {
			return fmt.Sprintf("vm %s bound to untracked lease %s", status.ID, status.Lease)
		}
		if lease.State.Terminal() {
			return fmt.Sprintf("vm %s bound to terminal lease %s", status.ID, status.Lease)
		}
	}
	return ""
}

// desired-not-collected: no VM or workspace is destroyed while its lease is
// still in the control plane's last desired set. This is the property that
// makes a rejected-spec-turned-orphan (a validation failure escalating to
// destruction of live customer state) a caught regression rather than a
// silent data-loss bug.
func checkDesiredNotCollected(state WorldState) string {
	present := map[string]bool{}
	for _, status := range state.VMs {
		if status.Lease != "" {
			present[status.Lease] = true
		}
	}
	for _, workspace := range state.Workspaces {
		if lease := workspaceLease(workspace.Name); lease != "" {
			present[lease] = true
		}
	}
	tracked := map[string]bool{}
	for _, lease := range state.Leases {
		tracked[lease.LeaseID] = true
	}
	for leaseID := range state.Named {
		// A lease the control plane named and that once had a resource must
		// still be tracked or still be backed by a live resource — never
		// silently reduced to nothing by GC.
		if state.HadResource[leaseID] && !tracked[leaseID] && !present[leaseID] {
			return fmt.Sprintf("named lease %s was collected while still wanted", leaseID)
		}
	}
	return ""
}

// workspaceLease extracts the lease a workspace volume name encodes.
func workspaceLease(name string) string {
	if i := strings.LastIndex(name, "/ws/"); i >= 0 {
		return name[i+len("/ws/"):]
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
// more than one tick's worth of slack. Quarantined leases are exempt: their
// lifecycle is deliberately frozen, and the agent resets the deadline clock
// when a parseable spec lifts the quarantine.
func checkDeadlineRelease(state WorldState) string {
	const slack = time.Second
	for _, lease := range state.Leases {
		if lease.Quarantined {
			continue
		}
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
		if !lease.State.Terminal() || lease.ExecutionID == "" {
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
		if lease.State.Terminal() && lease.VMID != "" {
			return fmt.Sprintf("terminal lease %s still holds vm %s", lease.LeaseID, lease.VMID)
		}
	}
	return ""
}
