# Postflight fleet

Status: end-state architecture, 2026-07-24. How Postflight models hardware:
two fleets, hardware classes as data, compatibility classes as the boundary of
warmth, and silicon onboarding as a routine rather than an event.

## Two fleets, two clouds

Each product runs on its own fleet in its own provider account:

| Fleet | Silicon | Provider posture |
| --- | --- | --- |
| Lightning | Bare-metal AMD Ryzen, highest available clocks | Host is trusted and hardened; provider diligence matters |
| Confidential | AMD EPYC with SEV-SNP | Host and provider are untrusted (see [security model](postflight-security-model.md)); provider choice is commercial |

Fleets share nothing at the data plane. Generations, capsules, and keys never
cross fleets — the fleets are different compatibility classes by
construction, so the question of moving warm state between them does not
arise.

Providers are fungible within a fleet. The control plane carries no
provider API coupling beyond provisioning; a host is `(site, hardware class,
capabilities, slots)` regardless of who rents it to us. On Confidential this
is a security property, not just an operational one: the byzantine-host model
means switching providers changes no claim.

## Hardware classes are rows

A **hardware class** is a database row, not a code path:

```text
hardware_class:
  id                    e.g. ryzen-9950x, epyc-9275f, epyc-venice-<model>
  fleet                 lightning | confidential
  cpu_family            microarchitecture generation
  qemu_cpu_model        the pinned guest-visible CPU baseline for this class
  cores / smt_policy    sellable real cores; SMT stance per class
  ram_gb / nvme_layout  slot geometry inputs
  tee                   none | sev-snp
  attestation_policy    VCEK family, minimum TCB per component (confidential only)
  launch_profile        the measured QEMU argv artifact for this class
```

Hosts carry `(hardware_class, site, capabilities, slot count)`. The scheduler
filters on class and capabilities; admission maps a runner label to a class.
Nothing in hostd or guestd branches on class identity — they consume the
launch profile and slot geometry they are handed.

## Compatibility classes bound warmth

A **compatibility class** is the domain inside which a CRIU capsule may
restore:

```text
compat_class = (qemu_cpu_model, machine type, guest image, CRIU format)
```

Every generation records its compatibility class in its manifest. Restore
requires an exact match; anything else is a cold build by construction, never
an error. Warmth never crosses a compatibility class, a fleet, or (on
Confidential) a chip.

**The guest CPU model is pinned per hardware class, not passed through.**
Passthrough would maximize guest-visible clocks but make every chassis its
own warmth island and every firmware quirk a restore hazard. A pinned
baseline per class keeps capsules portable across all hosts of the class at a
small ISA cost, keeps the launch profile deterministic, and — on
Confidential — keeps the measurement stable. The baseline is chosen per
class, as new as the class's silicon allows.

## Onboarding new silicon is a routine

When a new hardware generation becomes available (a new EPYC generation, a
faster Ryzen part, a new provider's chassis), onboarding is data and
evidence, never code:

1. **Rows.** Add the hardware class; register hosts with capabilities.
2. **Launch profile.** Produce and pin the class's QEMU argv artifact (CPU
   model, machine type, memory backend). On Confidential, record the
   resulting launch measurement per golden image.
3. **Attestation policy** (Confidential). Pin the VCEK family and minimum
   TCB derived from active AMD bulletins for the new part.
4. **Bench.** Run the standard rate-card suite on the class. Loadtests record
   numbers; a class without benchmark provenance has no SKU.
5. **Canary soak.** The production canary tenant runs its full scenario set
   against the class before any customer label maps to it.
6. **Sell.** Map runner-class labels to the hardware class in admission.

The first customer job in each scope on a new class is a cold build — a new
compatibility class starts empty by definition. That is the entire cost of a
hardware generation: no code, no migration, one cold build per scope.

Retiring a class is the same routine reversed: unmap labels, drain via
cordon, reap its generations. Chip-bound warmth dies with the class; that is
priced in by the regenerable-cache principle.

## Slot geometry

Capacity is fixed slots per host, set at provisioning:

- **CPU first.** Slots sell real cores. SMT policy is per class: siblings are
  either gang-scheduled to the same VM or SMT is off; a physical core is
  never shared across tenants. Concurrent-build interference, not VM count,
  sizes the slot count.
- **RAM is never overcommitted.** SNP memory is pinned at its high-water
  mark; Lightning follows the same rule for predictability. Host RAM math
  subtracts the ZFS ARC cap (`zfs_arc_max` is set at provisioning, or
  accounting lies) and per-VM QEMU overhead.
- **Disk is the overcommitted dimension.** Sparse zvols overcommit NVMe;
  hostd enforces refusal-only watermarks — refuse refill and materialization,
  never touch a running job. A host past its watermark reports degraded slots
  and drains.

## Sites

A site is one provider region for one fleet. Storage traffic (none in the
end-state hot path; provisioning and image distribution otherwise) stays
inside a site. There is no cross-site data plane and no cross-site warmth.
Capacity planning is per site: provision for peak, bill for use, no
autoscaler — adding hosts is a human decision informed by the capacity
ledger.

Related: [architecture](postflight-architecture.md) ·
[storage](postflight-storage.md) · [security model](postflight-security-model.md)
