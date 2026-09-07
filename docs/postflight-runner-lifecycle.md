# Postflight runner lifecycle

Status: current first-party Turbo path, 2026-09-06. Ubuntu 24.04, one job per
KVM VM, no SEV-SNP requirement. The confidential profile remains separate.

## Pool creation and generic boot reuse

1. Hostd checks native encryption and loaded keys throughout its managed ZFS
   subtree, then prepares a generic RAM/root template before starting its agent.
2. A template donor boots only to guestd readiness. It has no pool-member
   registration, assignment, tenant volumes, or customer processes.
3. Hostd pauses it, saves QEMU RAM/device state, destroys the donor, and seals
   the matching root snapshot. Template identity includes the host boot,
   exact QEMU and firmware bytes, image, machine/CPU model, network, and geometry.
4. Each pool VM clones that root and restores the generic RAM. Before JIT is
   passed to Listener, guestd refreshes entropy, clock, machine identity, the
   destination MAC, and DHCP state. Initialization failure keeps Listener
   blocked and fails the VM.
5. The control plane creates pools for observed class demand, and a fresh
   GitHub App JIT configuration registers each member. GitHub decides which
   connected Listener receives the job.

See [host startup](../src/postflight/hostd/cmd/hostd/main.go),
[template lifecycle](../src/postflight/hostd/vm/warm_template.go),
[guest initialization](../src/postflight/guestd/initialize_linux.go), and
[control-plane scheduler](../src/postflight/controlplane/scheduler.go).
The template is generic platform state, never a snapshot of a registered runner.

## Assignment and disk materialization

The patched Listener reports the acquired job identity before Worker starts.
The control plane joins that observation to a unique job intent. Its durable
assignment records the member incarnation, repository, run and attempt,
workflow job, and selected workspace scope/generation. GitHub REST job IDs
and runner protocol job IDs have different meanings and are not interchangeable.

```text
GitHub selects Listener
  -> observed assignment binds member to exact job
  -> verify scope and compatible generation
  -> clone and attach workspace/tool volumes
  -> converge host-zfs mounts
  -> start fresh customer process capsule
  -> authorize Worker
  -> GitHub receives normal runner job logs
```

Arbitrary customer-process CRIU restore remains disabled in the
[plan](../src/postflight/hostd/agent/plans.go) and
[sync](../src/postflight/hostd/agent/sync.go) gates. A reused disk generation
therefore does not mean restored compiler-process memory. The CRIU library's
restore-or-cold tests cover retained code, not an enabled CI path.

A scope, integrity, device, mount, or initialization failure keeps Worker
blocked and recycles the VM. A compatible cache miss uses empty disks and
fresh processes. Any VM loss after GitHub's `acquirejob` commit point cannot
transparently give the acquired message back to another Listener; the record
must not claim that attempt was requeued.

## Disk generation creation and publication

The [completion path](../src/postflight/hostd/agent/converge.go) is:

1. Worker completes and the guest flushes its durable filesystems.
2. Hostd destroys QEMU; the donor can no longer mutate its disks.
3. An empty process-checkpoint sentinel advances disk publication. No CRIU
   dump is taken and no customer process digest is published.
4. Only an eligible trusted branch scope receives a candidate generation.
5. Hostd seals the disk tuple and sends snapshot evidence.
6. The control plane requires the exact GitHub attempt's successful API
   conclusion, then promotes with a compare-and-swap against the prior scope
   head. Failure or ambiguity leaves the previous trusted head intact.
7. A new VM may clone the promoted workspace/tool state. The completed VM is
   never reused for another job and never becomes a RAM-template donor.

The [Turbo end-to-end test](../src/postflight/controlplane/turbo_e2e_test.go)
proves this policy with a real Postgres and fake substrate. It is not real
KVM or ZFS evidence.

## Proof and timing

The authenticated [operator projection](../src/postflight/controlplane/hostd_status.go)
shows host health, pool/member states, demands, assignments, and generation
lineage without credentials. Across two actual VM incarnations, compare the
first assignment's sealed generation with the next assignment's source
generation and scope head. Pair that with host `snapshot_seal_completed`
evidence and guest-visible persisted cache state.

RAM restore is a separate proof: the host must load the saved RAM/root tuple,
and two concurrent restored guests must have distinct MAC/IP and machine
identities and complete real GitHub jobs. A cold QEMU start or a shell timing
line is not that proof. Native GitHub logs must stream while the Worker runs.

Timing events carry source-local monotonic clocks and boot IDs; cross-source
spans use bracketed realtime samples and state their uncertainty. Report
GitHub queue/assignment time, boot restore, disk materialization, Worker
release, workload duration, and generation promotion separately. The full
acceptance contract is in [CI migration](postflight-ci-migration.md).
