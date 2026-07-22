# 0013 — Bind jobs after local runner assignment

Status: Accepted · Date: 2026-07-21

## Context

GitHub chooses an idle self-hosted runner after a job is acquired. A runner
scale-set listener can control which labels and how much capacity are offered,
but its acquire operation does not select an individual runner. Activating one
listener for each queued job would make the common multi-job workflow pay the
runner registration and assignment handshake serially.

Postflight keeps generic QEMU guests booted and their GitHub runner listeners
connected before a customer job exists. Customer storage and process state
cannot be attached until the selected guest is known. Predicting that selection
or repairing crossed predictions couples capacity to jobs and makes concurrent
same-label assignments ambiguous.

Process restore is an optimization over a durable workspace and tool
generation. CRIU may reject an otherwise authentic generation because a file,
descriptor, PID, socket, kernel feature, or CRIU format is incompatible with
the current capsule. That incompatibility must not turn a runnable customer job
into a failure. Authenticity, tenant binding, rollback, attestation, and cleanup
failures have a different meaning: continuing in the affected guest could cross
a security boundary.

## Decision

Postflight models runner supply, provider demand, local assignment, and durable
state as separate resources.

- A **pool member** is one booted guest and one connected, generic, single-use
  GitHub runner listener. It owns no repository identity or customer volume.
- A **job intent** is GitHub's numeric workflow job and check-run identity,
  plus the requested labels and workflow identity. It admits demand to the
  pool; it does not bind a member.
- An **assignment** is the immutable binding created when the patched runner
  listener reports the check-run ID, runner request ID, and protocol job ID
  from inside a particular guest before it creates Runner.Worker. The check-run
  ID joins directly to the queued provider job; the local observation selects
  the pool member.
- A **durable generation** is an authenticated tuple of workspace, tool, root,
  and optional process snapshots with one compatibility and attestation
  manifest. Its process component can be invalidated without invalidating an
  authentic workspace or tool component.

All eligible pool listeners remain online. GitHub may choose any of them. The
selected listener blocks Runner.Worker, reports the assignment locally, and
hostd immediately synchronizes that observation. The control plane returns the
generation selected for that immutable assignment. hostd hot-attaches its
zvols to the already selected guest, and guestd releases Runner.Worker only
after mount, restore or cold fallback, clock, and identity gates succeed.

Warm restore has three outcomes:

1. **restored** — adopt the restored capsule and continue;
2. **incompatible** — destroy and prove empty the partial capsule, invalidate
   only the process snapshot, create a cold capsule in the same live guest, and
   continue the same assigned Worker;
3. **unsafe** — for an integrity, authentication, tenant-binding, rollback,
   attestation, key-release, or unprovable-cleanup failure, keep Worker blocked,
   fail closed, and recycle the guest.

If QEMU or the guest actually dies, the connected runner dies with it. GitHub
requeues the job and assigns a different pool member; the replacement uses a
cold capsule because the failed process snapshot has been invalidated. “Same
Worker” applies only to a recoverable restore failure inside a still-healthy
guest.

Snapshot publication is ordered: stop and remove runner processes, freeze the
capsule, checkpoint it, flush the workspace and tool filesystems, snapshot the
coupled zvol tuple, destroy the donor guest, authenticate the manifest, and
only then publish the candidate after GitHub's attempt-specific success. A
partial or ambiguous sequence publishes nothing.

Every transition carries monotonic timestamps at its source and a realtime
clock bracket for cross-machine correlation. No latency calculation subtracts
monotonic clocks from different boot IDs.

## Consequences

- Concurrent jobs use all ready listeners without serialized activation.
- `runs-on`, runner group, labels, pool health, and capacity determine
  eligibility; none of them predicts the chosen guest.
- Display names and webhook arrival order are never assignment keys.
- Tenant volumes are absent from listening guests and attach only after the
  exact local binding exists.
- A process-cache miss or ordinary CRIU incompatibility changes performance,
  not job semantics.
- A cleanup failure cannot silently downgrade to a cold run in a contaminated
  guest.
- Pool members are single-use. Completion, cancellation, guest loss, and unsafe
  restore all recycle the guest and replenish the pool.
- The control plane can ingest scale-set events for earlier staging and richer
  telemetry, but correctness does not depend on an internet round trip before
  GitHub assigns the runner.

Related source: `src/services/postflight/generation/`,
`src/services/postflight/guestd/`, `src/services/postflight/hostd/`,
`src/services/postflight/controlplane/`
