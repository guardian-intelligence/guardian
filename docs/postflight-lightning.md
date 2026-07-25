# Lightning

Status: technology doctrine, 2026-07-24.

Lightning is the warmth substrate under every Postflight SKU: it makes a CI
job start warm, run on hot caches, and leave its work behind for the next
job. It is a technology, not a SKU — runner labels are plain
(`postflight-<x>vcpu-<os>-<flavor>`), and both categories behind them,
Turbo and Confidential, run the same substrate. Companion docs own the
mechanisms in detail; this document owns the rationale.

## Customer goals

Every mechanism below serves one of three customer-observable properties:

1. **Minimize queueing.** A job starts when GitHub assigns it: pools
   prewarmed, sessions attested, plans prepositioned. Up to reserved
   concurrency, P99 start time is P50.
2. **Maximize concurrency.** Slots are real cores, never oversubscribed.
   Capacity is fixed, refilled ahead of demand; destroy-and-refill keeps
   every slot immediately resalable. Concurrency is a guaranteed number.
3. **Fastest possible CI.** The highest-clock silicon we can buy, with
   source, caches, and long-lived build processes already present when the
   job lands. Warm start × hot cache × fast CPU compound.

## The four pillars

### 1. Prewarmed VMs

A generic guest is booted, attested, and listening before any customer
demand exists ([scheduling](postflight-scheduling.md), pool supply). When
GitHub assigns a job, only the hot path remains: observe the assignment,
attach the sticky disks, restore the capsule, open the Worker gate. Every
VM is single-use — one job, then destroyed and its slot refilled — so
warmth never trades against isolation.

### 2. Build artifacts persisted in zvols, mounted just in time

Workspace, tool caches, and build state persist as sparse zvol generations
on the worker's NVMe zpool ([storage](postflight-storage.md)).
Materializing a workspace from a sealed generation is a constant-time CoW
clone; volumes reach the running VM by virtio-scsi hot-attach in tens of
milliseconds. Source and caches are local block devices before the job's
first step, never a download protocol.

### 3. Snapshots restored in userspace, just in time

On green push-to-main, the guest's long-lived build processes — compilers,
daemons, watchers, warm JITs — are checkpointed by CRIU
(checkpoint/restore in userspace) into an encrypted process volume,
atomically coupled to the zvol generation set, and sealed as a signed
golden generation. The next job restores that capsule into a fresh
prewarmed VM. Restore-or-cold is the only branch — a miss costs speed,
never correctness ([runner lifecycle](postflight-runner-lifecycle.md)).

### 4. Node-local zvols on premium NVMe

All warm state lives on the worker's own striped NVMe zpool. No network
storage on any hot path: no Ceph, no object-store round trip, no cache
download. The cost: warm state is a regenerable cache, host loss is one
cold build, and nothing is replicated or migrated
([storage](postflight-storage.md), locality).

## The mindset

Each pillar corrects an operating assumption:

- **Stop discarding CI's work.** Every run compiles, fetches, and warms;
  stock runners throw it away at job end. Persisting artifacts — scoped,
  sealed, promoted on green — makes each run the starting line for the
  next.
- **Use existing compute better.** Not more shared vCPUs: real cores at
  high clocks with hot caches. A warm start on reserved hardware beats an
  autoscaled cold fleet on both latency and cost.
- **Treat CI like a developer machine.** CI catches only a subset of
  problems; waiting 20+ minutes for it is a bad trade. A developer machine
  builds incrementally from yesterday's state; Lightning gives CI the same
  property — and gives agents a golden workspace: a cold VM starts from the
  sealed green-on-main state instead of warming caches for 20 minutes.

## Who Lightning is for

Agentic engineers, specifically:

- Engineers who want to unhobble their agents — agents multiply job volume
  ahead of headcount, and the feedback loop is the bottleneck.
- Engineers who understand CI shouldn't persist secrets and will make the
  few changes to keep that true. Lightning enforces its half by
  construction: runner processes are killed and proven absent before any
  capsule freezes, credentials never touch disk, and everything persisted
  is ciphertext ([security model](postflight-security-model.md)).
- Engineers who want to run code in a TEE — Confidential runs the
  identical substrate inside SEV-SNP, where a compromised host reads
  nothing.

## Measured baselines

Tracer-measured on guardian-w1 NVMe, 2026-07-05. Per-class production
numbers come from the rate-card bench and carry benchmark provenance
before any claim ships ([fleet](postflight-fleet.md), onboarding).

| Operation | Measured |
| --- | --- |
| Full warm restore | ~520 ms (vs 8.8 s cold boot) |
| Parallel restore | 8 restores in 774 ms wall (~10 VMs/s) |
| Sticky-disk hot-attach | 227 ms, revoke verified |
| Workspace materialization (CoW clone) | Constant-time metadata, ~tens of ms at any size |

## What Lightning is not

- Not a SKU, a fleet, or a hardware class — those are rows and labels; the
  fleets are named Turbo and Confidential.
- Not durable storage. Warm state is a regenerable cache; anything that
  would complicate that — replication, migration, backup, cross-host key
  release — is a cold build instead.
- Not a second warmth mechanism. One mechanism: CRIU capsules on sticky
  zvol generations, identical on both fleets
  ([architecture](postflight-architecture.md), principle 5).
- Not a scheduler. GitHub owns the DAG and runner selection; Lightning
  makes whichever guest wins the assignment already warm.

Related: [architecture](postflight-architecture.md) ·
[fleet](postflight-fleet.md) · [storage](postflight-storage.md) ·
[scheduling](postflight-scheduling.md) · [host](postflight-host.md) ·
[security model](postflight-security-model.md)
