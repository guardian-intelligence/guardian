package agent

import (
	"context"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
)

func (a *Agent) recycleUnownedUnusableVMs(ctx context.Context, view *vmView, assignments map[string]*assignment) {
	for id, status := range view.byID {
		if status.Phase != vm.PhaseRecycleRequired && status.Phase != vm.PhaseExited {
			continue
		}
		if assignmentOwnsMember(assignments, status.MemberID) || status.Assignment.RequestID != "" {
			continue
		}
		if err := a.vms.Destroy(ctx, id); err != nil {
			a.logger.Error("recycling unusable pool vm", "member_id", status.MemberID, "vm", id, "reason", status.FailureReason, "err", err)
			continue
		}
		delete(view.byID, id)
		delete(view.byMember, status.MemberID)
		view.countByCl[status.Class]--
	}
}

// reconcilePool converges warm-VM counts toward the control plane's targets
// within this host's fixed slot totals. Destroy-and-refill is the governor's
// whole vocabulary: it launches fresh VMs and destroys idle ones, never
// touches a busy member, and never reuses anything.
func (a *Agent) reconcilePool(ctx context.Context, vms *vmView, poolTargets map[vm.Class]int, assignments map[string]*assignment) {
	// A host configuration can stop serving a class while idle VMs from its
	// previous configuration still exist on disk. Busy members finish under
	// their original class and are reclaimed by their assignment lifecycle.
	for id, status := range vms.byID {
		if _, configured := a.cfg.Slots[status.Class]; configured || !poolVMCanRecycle(status, assignments) {
			continue
		}
		if err := a.vms.Destroy(ctx, id); err != nil {
			a.logger.Error("destroying obsolete-class pool vm", "vm", id, "class", status.Class, "err", err)
			continue
		}
		delete(vms.byID, id)
		vms.countByCl[status.Class]--
	}

	for class, total := range a.cfg.Slots {
		target := poolTargets[class]
		if target > total {
			target = total
		}
		// An image rollout never offers stale idle capacity to an assignment. Old
		// metadata predating the image field also compares unequal and is
		// replaced. Busy members finish under the image they started with.
		if image := a.cfg.Images[class]; image != "" {
			for id, status := range vms.byID {
				if status.Class != class || status.Image == image || !poolVMCanRecycle(status, assignments) {
					continue
				}
				if err := a.vms.Destroy(ctx, id); err != nil {
					a.logger.Error("destroying stale-image pool vm",
						"vm", id, "have", status.Image, "want", image, "err", err)
					continue
				}
				delete(vms.byID, id)
				vms.countByCl[class]--
				if status.Phase == vm.PhaseWarm {
					ids := vms.warm[class]
					for i, warmID := range ids {
						if warmID == id {
							vms.warm[class] = append(ids[:i], ids[i+1:]...)
							break
						}
					}
				}
			}
		}
		// vms.warm reflects claims made earlier in this same tick; booting
		// VMs count toward the target because they are refills in flight.
		warmish := len(vms.warm[class])
		for _, status := range vms.byID {
			if status.Class == class && status.MemberID == "" && status.Phase == vm.PhaseBooting {
				warmish++
			}
		}

		// Refill up to the target, bounded by free slots.
		for warmish < target && vms.countByCl[class] < total {
			id := vm.ID("pool-" + a.newID())
			if err := a.vms.Launch(ctx, id, class); err != nil {
				a.logger.Error("launching pool vm", "class", class, "err", err)
				break
			}
			warmish++
			vms.countByCl[class]++
		}

		// Shed surplus warm VMs, oldest-ID first for determinism. Booting
		// VMs finish booting before they are eligible; a target of zero
		// (cordon) drains as they turn warm.
		if warmish > target {
			surplus := warmish - target
			for _, id := range vms.warm[class] {
				if surplus == 0 {
					break
				}
				if err := a.vms.Destroy(ctx, id); err != nil {
					a.logger.Error("destroying surplus warm vm", "vm", id, "err", err)
					continue
				}
				surplus--
				vms.countByCl[class]--
			}
		}
	}
}

func poolVMCanRecycle(status vm.Status, assignments map[string]*assignment) bool {
	if status.Assignment.RequestID != "" || assignmentOwnsMember(assignments, status.MemberID) {
		return false
	}
	switch status.Phase {
	case vm.PhaseBooting, vm.PhaseWarm, vm.PhaseAssigned, vm.PhaseListening:
		return true
	default:
		return false
	}
}
