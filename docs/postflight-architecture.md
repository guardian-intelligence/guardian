# Postflight production architecture

Status: end-state architecture, 2026-07-24.

Companions:

- [Fleet](postflight-fleet.md) — hardware classes, warmth domains, the two clouds
- [Lightning](postflight-lightning.md) — the warmth substrate: prewarmed VMs, sticky disks, userspace restore, node-local NVMe
- [Security model](postflight-security-model.md) — per-product threat models and claims
- [Scheduling](postflight-scheduling.md) — control plane, state, admission, assignment
- [Storage](postflight-storage.md) — sticky disks, generations, sealing, locality
- [Host](postflight-host.md) — hostd, the QEMU profile, the guest contract
- [Runner lifecycle](postflight-runner-lifecycle.md) — the operational model per job
- [ADR 0013](adrs/0013-bind-jobs-after-local-runner-assignment.md) — assignment is observed, never predicted

## Two SKU categories, three axes

Postflight competes on three axes:

| Axis | How we win | Load-bearing architecture |
| --- | --- | --- |
| Speed | Warm starts in milliseconds, not minutes | CRIU process capsules + sticky ZFS disks (constant-time CoW clones) on high-clock bare metal |
| Security | Hardware-enforced job isolation a compromised host cannot pierce | SEV-SNP guests, in-guest keys, attestation-gated release |
| Features | A full machine, not a stripped microVM | Full QEMU: complete device surface, `/dev/kvm`, dockerd parity, SSH, hot-attach |

Two SKU categories on two separate clouds, both on the
[Lightning](postflight-lightning.md) warmth substrate. SKUs are runner
labels `postflight-<x>vcpu-<os>-<flavor>`; the flavor selects the category.
The non-TEE flavor's public name is provisional (`turbo` until ruled
otherwise):

| | Turbo | Confidential |
| --- | --- | --- |
| Runner label | `postflight-<x>vcpu-ubuntu24-turbo` | `postflight-<x>vcpu-ubuntu24-confidential` |
| Silicon | Bare-metal AMD Ryzen (high clock) | AMD EPYC with SEV-SNP |
| Cloud | Ryzen bare-metal provider | Latitude (current) |
| TEE | None — the silicon has no SEV | SEV-SNP, always on |
| Warmth | CRIU capsule + sticky disks | CRIU capsule + sticky disks |
| At-rest keys | OpenBao Transit custody | Derived inside the CPU |
| `/dev/kvm` | Yes | No (impossible in SNP guests) |
| Host trust | Trusted, hardened | Untrusted conduit |

The split is hardware-honest: Ryzen has no SEV, so Turbo claims speed and
isolation, never confidentiality; SNP forbids `/dev/kvm`, so KVM-needing
jobs route to Turbo. Nobody else offers a TEE and KVM on one platform.

Full QEMU is the only VMM that carries all three axes: SEV-SNP launch,
tens-of-milliseconds virtio-scsi hot-attach of sticky disks, and the
complete device model. "Custom QEMU" means a Guardian-built, pinned,
attested **upstream** artifact plus an owned launch profile per hardware
class — never a fork. Scheduling, storage, and lifecycle logic live in
Guardian daemons.

## The assembly

Three Guardian processes, four pieces of infrastructure, one external
scheduler.

```text
GitHub webhooks/API ──► Control plane ──plans/prefetch──► hostd (one per host)
       │                     │                              │
       │                     ├─ Postgres: four schemas      ├─ OpenZFS: sticky disks
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
| GitHub | Workflow DAG, retries, runner selection. The only workflow engine in the system. |
| Control plane | One deployable binary: admission, job plans, assignment truth, generation catalog, attested sessions, key custody, metering, reconcilers, the production canary. |
| Postgres | Four independently owned schemas: capacity, demand/assignment, storage, usage. Ordinary relational rows updated in place; history only where it pays (usage intervals, assignment identity). Short transactions, idempotency keys, `FOR UPDATE SKIP LOCKED` workers. |
| OpenBao | Product-scoped Transit mount (`transit-postflight`): Turbo DEK wrap/unwrap, Confidential tenant key custody, generation-manifest signing, per-tenant crypto-erase. |
| hostd | Per-host daemon: slot actors, storage manager, QEMU supervision, checkpoint sealing, crash-safe operation journal, two-lane control stream. |
| guestd | The only privileged agent in the guest: attestation, LUKS and mounts, runner supervision, the Worker gate, the CRIU capsule, quiesce. |
| QEMU + OpenZFS | Mechanism, never policy. Pinned QEMU per fleet; node-local NVMe zpools; no network storage on any hot path. |

## Principles

1. **GitHub is the scheduler.** No internal workflow engine duplicates its
   DAG. Webhooks are hints (delivery and order are unreliable), the REST API
   is truth, the guest's locally observed assignment is the final fallback.
2. **Assignment is observed, never predicted** (ADR 0013). All listeners
   stay connected; GitHub picks one; the selected guest reports the binding
   before Runner.Worker exists; prepositioned plans mean the winner needs
   no round trip.
3. **One job, one VM, destroy-and-refill.** Pool members are single-use;
   completion, cancellation, loss, and unsafe restore all recycle the guest.
4. **Warm state is a regenerable cache, never data.** Any miss, host loss,
   image roll, or key rotation costs exactly one cold build. Nothing in the
   warmth path is backed up, migrated, or recovered.
5. **One warmth mechanism.** CRIU process capsules on sticky zvol
   generations, identical on both fleets. Whole-VM snapshots do not exist:
   SNP forbids them, and a second mechanism would fork the seal pipeline,
   the manifest, and the compatibility story.
6. **The hot path belongs to one slot.** Between assignment observation and
   Worker authorization, only the owning slot actor runs — no pool scan,
   inventory report, GC, or control-plane convergence.
7. **Hardware is data.** New silicon (a hardware class, an EPYC generation,
   a provider) is onboarded by adding rows, benching, and setting
   attestation policy, not by writing code. Warmth is bounded by
   compatibility classes and never crosses them.
8. **Keys have one custodian per fleet.** Confidential: the CPU derives
   volume keys in-guest; they never cross the guest boundary in either
   direction. Turbo: `transit-postflight` custodies lineage DEKs, and a
   tenant's Transit key is its crypto-erase switch.
9. **On Confidential, the host is a conduit.** Secret-bearing traffic
   between control plane and guest is sealed to attestation; hostd relays
   ciphertext it cannot open. A compromised host reads nothing it was not
   already entitled to operate.
10. **Small schemas, not a god-object.** Capacity, demand/assignment,
    storage, and usage are independently owned Postgres schemas with small
    per-resource state machines; each controller advances only its own
    resource. Ordinary relational state, not event sourcing: rows update in
    place; append-only records exist only for usage intervals and
    assignment identity.
11. **Every claim ships with a gate.** Speed claims carry benchmark
    provenance; security claims carry release gates with positive controls.
    A claim without a falsifier does not go on the website.
12. **One IDL.** Every internal channel is generated from one protobuf
    package: control plane ↔ hostd is two gRPC streams per host
    (assignment/plan on one lane, inventory/telemetry on the other, so
    urgent messages never queue behind bulk); hostd ↔ guestd speaks the
    same generated protocol over vsock. A hand-framed message anywhere is a
    bug.

## What does not exist

- No workflow engine, no per-job Kubernetes objects, no host leases.
- No whole-VM snapshots; no second warmth mechanism.
- No cross-host generation replication, no key-release plane for moving
  warm state between chips: warmth is host-affine and a miss runs cold. The
  catalog and manifest keep the shape (wrapped-key reference, lineage,
  pointer CAS), so portable warmth would be a key-plane change, not a
  schema migration — adopted only on measured pull.
- No QEMU fork.
- No durable object-storage tier for customer state (ADR 0005, ADR 0009):
  sticky disks are node-local NVMe; their loss is a cold build.
