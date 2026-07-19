package agent

import (
	"context"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
)

// reconcilePool converges warm-VM counts toward the control plane's targets
// within this host's fixed slot totals. Destroy-and-refill is the governor's
// whole vocabulary: it launches fresh VMs and destroys idle ones, never
// touches an assigned VM, and never reuses anything.
func (a *Agent) reconcilePool(ctx context.Context, vms *vmView) {
	for class, total := range a.cfg.Slots {
		target := a.poolTargets[class]
		if target > total {
			target = total
		}
		// An image rollout never offers stale idle capacity to a lease. Old
		// metadata predating the image field also compares unequal and is
		// replaced. Assigned VMs finish under the image they started with.
		if image := a.cfg.Images[class]; image != "" {
			for id, status := range vms.byID {
				if status.Class != class || status.Lease != "" || status.Image == image {
					continue
				}
				if status.Phase != vm.PhaseWarm && status.Phase != vm.PhaseBooting {
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
			if status.Class == class && status.Lease == "" && status.Phase == vm.PhaseBooting {
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
