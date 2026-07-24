# Postflight runner lifecycle

This document is the current operational model for the Linux x64 Ubuntu 24.04
confidential runner. It assumes SEV-SNP, one job per VM, a pre-booted QEMU pool,
and encrypted node-local zvol generations.

## Four durable identities

| Resource | Stable identity | Created | Terminal condition |
| --- | --- | --- | --- |
| Pool member | `(host, vm, incarnation)` | A generic guest is launched | Its single job ends or the guest is recycled |
| Job intent | `(scale set, runner request ID, protocol job ID)` | `JobAvailable` is received | GitHub completes or cancels the request |
| Assignment | `(protocol job ID, member incarnation)` | The selected guest reports the job locally | The job ends, is withdrawn, or fails closed |
| Generation | Authenticated manifest digest and monotonic generation number | A trusted successful donor is sealed | Retention reaps it or policy invalidates it |

A numeric GitHub REST job ID is useful for UI and conclusion reconciliation,
but it is not the runner protocol job ID and is not used to decide which VM
received a job.

## Pool and assignment state

```text
pool member
  provisioning -> listening -> assigned -> rendezvous -> running -> recycling
        |              |           |            |            |
        +--------------+-----------+------------+------------+-> lost

job intent
  available -> acquired -> assigned -> running -> completed
       |           |          |          +-------> cancelled
       +-----------+----------+------------------> cancelled

assignment
  observed -> resolving -> binding -> restoring -> authorizing -> running
       |          |          |           |              |           |
       +----------+----------+-----------+--------------+-----------> terminal
```

An assignment row is append-oriented: its member, request ID, protocol job ID,
runner name, repository, run, attempt, and workflow job cannot be rewritten.
Only its state, selected generation, restore result, terminal result, and timing
evidence advance.

The runner listener is connected while the member is `listening`. GitHub's
assignment reaches the guest directly. The listener invokes guestd before
Runner.Worker dispatch; guestd blocks the listener until hostd completes the
rendezvous. This makes assignment deterministic after the fact without
serializing listener registration.

## Restore transaction

```text
verify manifest and scope
  -> attach authenticated workspace/tool/process volumes
  -> converge mounts
  -> attempt process restore
       -> success: adopt capsule
       -> incompatible:
            destroy partial capsule
            prove restore isolation is empty
            replace the cgroup boundary
            invalidate process component
            create cold capsule
       -> unsafe:
            keep Worker blocked
            recycle VM
  -> sample synchronized clock
  -> publish capsule PID
  -> release Worker
```

The restore isolation is disposable. CRIU runs with its process tree confined
to the capsule PID namespace and cgroup. A recoverable error is allowed to
continue cold only after the process tree is empty, every temporary mount is
detached, and the killed cgroup has been replaced with a distinct cgroup
object. Failure to prove or replace that boundary is an unsafe outcome.

### Failure policy

| Evidence | Disposition | Process snapshot | Customer job |
| --- | --- | --- | --- |
| CRIU format/version or kernel feature mismatch | Cold fallback | Invalidate | Continue on same live Worker |
| Missing/restale file, unsupported FD, PID or socket conflict | Cold fallback | Invalidate | Continue on same live Worker |
| CRIU exits unsuccessfully and cleanup is proven | Cold fallback | Invalidate | Continue on same live Worker |
| Snapshot digest, signature, tenant/scope, rollback floor, measurement, TCB, or key binding mismatch | Recycle VM | Quarantine generation | Never release this Worker |
| Cleanup cannot prove an empty capsule | Recycle VM | Invalidate and quarantine evidence | Never release this Worker |
| QEMU, guestd, or listener dies before provider acquisition | Recycle/refill | Invalidate suspect process component | GitHub requeues after its pickup deadline |
| QEMU, guestd, or listener dies after provider acquisition | Recycle/refill | Invalidate suspect process component | Current attempt cannot be transparently requeued |
| Cold capsule creation fails | Recycle VM | Already invalidated | Current attempt cannot be transparently requeued |

The workspace and tool snapshots survive a process-only invalidation if their
authenticated manifest components remain valid. The next attempt gets their
artifacts with a cold process capsule.

GitHub's broker `acquirejob` call is a commit point. Before it, disconnecting a
listener leaves the job eligible for GitHub's normal pickup retry. After it,
the provider does not expose a release operation that can assign the same job
message to another listener. The durable assignment ledger must not describe
post-acquisition VM loss as requeued; transparent recovery would require a
separately designed, attested handoff of the acquired job message and its
listener state to a replacement VM.

## Generation creation and publication

One manifest couples the workspace ZFS snapshot GUID, root and tool volume
generations, process image digest, guest image and kernel digests, QEMU/CRIU
format versions, CPU compatibility, SNP measurement and minimum TCB, tenant,
repository, branch, monotonic generation number, and the fleet's key
reference (derivation salt on Confidential, wrapped DEK on Lightning).

The donor sequence is:

1. GitHub's runner finishes and its credential-bearing processes are killed.
2. The capsule is frozen and CRIU writes its process image.
3. The guest flushes every mounted durable filesystem.
4. hostd destroys QEMU; a live donor can no longer mutate the tuple.
5. hostd snapshots every zvol and records their ZFS GUIDs.
6. The control plane authenticates the complete manifest as a candidate.
7. Attempt-specific GitHub success promotes it with a scope-pointer CAS.

Any failure skips publication. Snapshotting the runner itself, resuming the
donor after publication, or promoting a tuple with mismatched component
generations is forbidden.

## Timing contract

Each source records `CLOCK_BOOTTIME`, boot ID, sequence, and realtime. Durations
within a process use only its monotonic values. Cross-source spans use bracketed
realtime samples and report their uncertainty. Required hot-path events are:

```text
github_job_available
github_job_acquired
guest_assignment_observed
host_assignment_ingested
generation_selected
zvol_materialization_started/completed
qmp_attach_started/completed
guest_rendezvous_received
mounts_ready
restore_started
restore_succeeded
  | generation_restore_failed -> restore_cleanup_started/completed
      -> cold_capsule_start_started/completed
  | restore_unsafe
clock_checked
worker_released
customer_steps_released
```

Reports separate GitHub queue/assignment time, Postflight rendezvous time,
restore or cold-fallback time, and customer workload time.
