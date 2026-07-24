# Postflight production architecture

Status: end-state architecture, 2026-07-24. This document and its companions
describe the system Postflight converges to in production. They are living
documents: when a decision changes, the document changes with it — no
compatibility prose, no history.

Companions:

- [Fleet](postflight-fleet.md) — hardware classes, warmth domains, the two clouds
- [Security model](postflight-security-model.md) — per-product threat models and claims
- [Scheduling](postflight-scheduling.md) — control plane, ledgers, admission, assignment
- [Storage](postflight-storage.md) — sticky disks, generations, sealing, locality
- [Host](postflight-host.md) — hostd, the QEMU profile, the guest contract
- [Runner lifecycle](postflight-runner-lifecycle.md) — the operational model per job
- [ADR 0013](adrs/0013-bind-jobs-after-local-runner-assignment.md) — assignment is observed, never predicted

## Two products, three axes

Postflight competes on exactly three axes, and each axis is owned by a
deliberate piece of the architecture:

| Axis | How we win | Load-bearing architecture |
| --- | --- | --- |
| Speed | Warm starts measured in milliseconds, not minutes | CRIU process capsules + sticky ZFS disks (constant-time CoW clones) on high-clock bare metal |
| Security | Hardware-enforced job isolation a compromised host cannot pierce | SEV-SNP guests, in-guest keys, attestation-gated release |
| Features | A full machine, not a stripped microVM | Full QEMU: complete device surface, `/dev/kvm`, dockerd parity, SSH, hot-attach |

These axes are delivered by **two products on two separate clouds**:

| | Lightning | Confidential |
| --- | --- | --- |
| Silicon | Bare-metal AMD Ryzen (high clock) | AMD EPYC with SEV-SNP |
| Cloud | Ryzen bare-metal provider | Latitude (current) |
| TEE | None — the silicon has no SEV | SEV-SNP, always on |
| Warmth | CRIU capsule + sticky disks | CRIU capsule + sticky disks |
| At-rest keys | OpenBao Transit custody | Derived inside the CPU |
| `/dev/kvm` | Yes | No (impossible in SNP guests) |
| Host trust | Trusted, hardened | Untrusted conduit |

The split is hardware-honest: Ryzen parts have no SEV, so Lightning claims
speed and isolation, never confidentiality; SNP forbids `/dev/kvm`, so
KVM-needing jobs route to Lightning. Nobody else offers a TEE and KVM on one
platform — we offer both, one label apart.

Full QEMU is not incidental. It is the only VMM that carries all three axes at
once: SEV-SNP launch (security), virtio-scsi hot-attach of sticky disks in
tens of milliseconds (speed), and the complete device model (features).
"Custom QEMU" means a Guardian-built, pinned, attested **upstream** artifact
plus a rigorously owned launch profile per hardware class — never a fork.
Scheduling, storage, and lifecycle logic live in Guardian daemons.

## The assembly

Three Guardian processes, four pieces of infrastructure, one external
scheduler. Nothing else.

```text
GitHub webhooks/API ──► Control plane ──plans/prefetch──► hostd (one per host)
       │                     │                              │
       │                     ├─ Postgres: four ledgers      ├─ OpenZFS: sticky disks
       │                     ├─ OpenBao: transit-postflight ├─ QEMU: pinned artifact
       │                     ├─ attested sessions           └─ one SlotActor per slot
       │                     ├─ admission / planning                 │
       │                     └─ metering / reconcilers               │ vsock
       │                                                             │
       └── assignment ──► selected Runner.Listener ──► guestd ───────┘
                                                        │
                                                        ├─ LUKS + mount ladder
                                                        ├─ CRIU capsule
                                                        └─ Worker gate
```

| Component | Owns |
| --- | --- |
| GitHub | The workflow DAG, retries, and runner selection. The only workflow engine in the system. |
| Control plane | One deployable binary. Ledgers, admission, job plans, assignment truth, generation catalog, attested sessions, key custody, metering, reconcilers, the production canary. |
| Postgres | Four independent ledgers: capacity, demand/assignment, storage, usage. Inbox/outbox rows, short transactions, idempotency keys, `FOR UPDATE SKIP LOCKED` workers. |
| OpenBao | Product-scoped Transit mount (`transit-postflight`): Lightning DEK wrap/unwrap, Confidential tenant key custody, generation-manifest signing, per-tenant crypto-erase. |
| hostd | Per-host daemon. Slot actors, storage manager, QEMU supervision, checkpoint sealing, a crash-safe operation journal, one prioritized control stream. |
| guestd | The only privileged agent in the guest. Attestation, LUKS and mounts, runner supervision, the Worker gate, the CRIU capsule, quiesce. |
| QEMU + OpenZFS | Mechanism, never policy. Pinned QEMU per fleet; node-local NVMe zpools; no network storage on any hot path. |

## Principles

Each principle is stated so that a violation is observable.

1. **GitHub is the scheduler.** No internal workflow engine duplicates its
   DAG. Webhooks are hints (order and delivery are unreliable), the REST API
   is truth, and the guest's locally observed assignment is the final
   correctness fallback.
2. **Assignment is observed, never predicted** (ADR 0013). All listeners stay
   connected; GitHub picks one; the selected guest reports the binding before
   Runner.Worker exists; plans are prepositioned so the winner needs no
   round trip.
3. **One job, one VM, destroy-and-refill.** Pool members are single-use. No
   VM accepts a second job; completion, cancellation, loss, and unsafe
   restore all recycle the guest.
4. **Warm state is a regenerable cache, never data.** Any miss, host loss,
   image roll, or key rotation costs exactly one cold build. Nothing in the
   warmth path is backed up, migrated, or recovered.
5. **One warmth mechanism.** CRIU process capsules on sticky zvol
   generations, identical on both fleets. Whole-VM snapshots do not exist
   anywhere: SNP forbids them, and a second mechanism would fork the seal
   pipeline, the manifest, and the compatibility story.
6. **The hot path belongs to one slot.** Between assignment observation and
   Worker authorization, only the owning slot actor runs. No pool scan,
   inventory report, GC, or control-plane convergence may appear on that
   path.
7. **Hardware is data.** New silicon (a hardware class, a new EPYC
   generation, a new provider) is onboarded by adding rows, benching, and
   setting attestation policy — never by writing code. Warmth is bounded by
   compatibility classes and never crosses them.
8. **Keys have one custodian per fleet.** Confidential: the CPU derives
   volume keys in-guest and they never cross the guest boundary in either
   direction. Lightning: `transit-postflight` custodies lineage DEKs and a
   tenant's Transit key is its crypto-erase switch.
9. **On Confidential, the host is a conduit.** Secret-bearing traffic
   between the control plane and the guest is sealed to attestation; hostd
   relays ciphertext it cannot open. A compromised host reads nothing it was
   not already entitled to operate.
10. **Ledgers, not a god-object.** Capacity, demand/assignment, storage, and
    usage are independent ledgers with small per-resource state machines.
    Each controller advances only the resource it owns.
11. **Every claim ships with a gate.** Speed claims carry benchmark
    provenance; security claims carry release gates with positive controls.
    A claim without a falsifier does not go on the website.

## What does not exist

Deliberate absences, so they are not "discovered missing":

- No workflow engine, no per-job Kubernetes objects, no host leases.
- No whole-VM snapshots; no second warmth mechanism.
- No cross-host generation replication and no key-release plane for moving
  warm state between chips: warmth is host-affine and a miss runs cold. The
  catalog and manifest keep the shape (wrapped-key reference, lineage,
  pointer CAS) so portable warmth would be a key-plane change, not a schema
  migration — it is adopted only on measured pull.
- No QEMU fork.
- No durable object-storage tier for customer state (ADR 0005, ADR 0009):
  sticky disks are node-local NVMe, and their loss is a cold build.
