# Postflight host

Status: end-state architecture, 2026-07-24. The worker host: hostd's
controllers, the QEMU profile doctrine, and the guest contract. On
Confidential the host is an untrusted conduit; everything here is written so
that property holds by construction, not by review.

## Host substrate

Each worker is a standalone machine — never a member of any management
cluster, holding no shared credentials. It runs exactly: the OS, hostd, the
pinned QEMU artifact, and OpenZFS. Hosts talk to the control plane by
dialing out (no inbound control ports) and to nothing else.

## hostd

hostd is a set of independent controllers over shared drivers. The rule that
shapes all of them: **the hot path belongs to one slot.** Between assignment
observation and Worker authorization, no controller may take a lock, run a
scan, or wait on convergence outside the owning slot.

| Controller | Owns |
| --- | --- |
| ControlStream | One persistent, host-initiated, mutually authenticated stream to the control plane. Two priority classes: assignment and plan traffic preempts inventory and telemetry. On Confidential it additionally relays sealed frames it cannot open. |
| SlotGovernor | The host's fixed slots: CPU sets, memory, NUMA, cgroup limits, SMT policy. Slots are configured at provisioning and never overcommitted. |
| SlotActor (one per slot) | A serialized event loop owning one slot's entire lifecycle: refill, rendezvous, authorization, seal, destroy. A hung operation in one slot stalls only that slot. |
| AssignmentRouter | Consumes the guest-observed assignment and resolves the prepositioned plan locally — no network round trip stands between assignment and storage work. |
| StorageManager | Clones, holds, snapshots, canonical zvol devices, inventory. Sealed generations are deleted only on a control-plane reap verb; derived state is GC'd freely. |
| QEMUSupervisor | Launch, QMP, hot-attach/detach by stable serial, network attachment, adoption, destruction. VM identity is on disk before side effects, so a restarted hostd adopts running VMs instead of orphaning them. |
| CheckpointSealer | The seal pipeline's host half: quiesce coordination, donor destruction, single-txg tuple snapshot, manifest evidence assembly. |
| OperationJournal | Crash-safe local intent/result records around every QEMU and ZFS side effect. Recovery replays or rolls forward from the journal; no database lock is ever held across a substrate operation. |
| Telemetry | Source-local monotonic events with boot IDs and bracketed realtime samples. Durations never subtract monotonic clocks from different boots. |

Watermarks are refusal-only: a host past its disk or memory watermark
refuses refill and materialization, reports degraded slots, and never
touches a running job.

## QEMU doctrine

- **A pinned artifact, not a fork.** Guardian builds, pins, and attests one
  upstream QEMU per fleet. Business logic lives in hostd and guestd; the VMM
  carries mechanism only.
- **One launch profile per hardware class**, rendered deterministically:
  machine type, the class's pinned guest CPU model, memory backend, device
  set. On Confidential the argv is measurement-bearing — a changed flag is a
  changed measurement is a fleet-visible event.
- VMs run as detached systemd transient scopes: hostd crash or upgrade never
  kills a running job; scopes provide cgroup limits, OOM containment, and
  per-VM accounting.
- The QEMU process is jailed per the §16 doctrine: non-root, seccomp
  sandbox with spawn denied, resource-limited. Network taps are created by a
  root-owned helper outside the QEMU process, attached through a fail-closed
  bridge hook with per-port isolation; guests get default-deny egress
  shaping and no guest-to-guest path.
- Guest devices arrive by stable serial (workspace, tool, process) via
  virtio-scsi hot-attach; attach and detach observe before acting so repeats
  converge. `/dev/kvm` is exposed only by Lightning class profiles.
- No QEMU guest agent, no serial shell, no host-commandable exec or file
  interface exists in any profile.

## The guest contract

guestd is the only privileged agent inside the guest, supervised by nothing
(a dead guestd is a dead VM). The single guest↔host channel is a closed
vsock protocol with bounded message sizes — the guest is untrusted input to
the host, and on Confidential the host is untrusted transport to the guest.

Boot ladder, before any customer demand exists:

1. Boot from the measured image (read-only root, dm-verity).
2. Confidential: request the SNP report over an ephemeral key and establish
   the sealed session with the control plane through the host conduit.
   Receive the JIT configuration and tenant key half over it. Lightning:
   receive the JIT configuration and lineage DEK over the authenticated
   control channel.
3. Start and supervise Runner.Listener with the JIT configuration held only
   in RAM and process environment; scrub it from any observable output.

Job ladder:

1. The patched listener reports the acquired job's full identity to guestd
   **before** Runner.Worker exists; guestd blocks the Worker gate and
   publishes the assignment (sealed-session-authenticated on Confidential).
2. Volumes arrive; guestd opens LUKS (fleet-appropriate key path), converges
   the mount tree as a dependency graph, and fails closed on any mode or
   tenant mismatch.
3. CRIU restore has exactly three outcomes: **restored** (adopt the
   capsule); **incompatible** (destroy the partial capsule, prove the
   boundary empty, replace it, invalidate only the process component, run
   cold on the same live Worker); **unsafe** (integrity, tenant, rollback,
   attestation, key, or cleanup failure — the Worker stays blocked and the
   guest is recycled). A cache miss changes performance, never job
   semantics.
4. Authorization closes the Worker gate exactly once; the Worker enters the
   capsule namespace with all inheritable capabilities dropped. Credentials
   never touch disk.

The capsule is the checkpointable unit: a PID and mount namespace with a
secretless init, confined to its own cgroup, holding only the tenant's
long-lived build processes. Before any dump, every runner process is killed
and proven absent — a capsule cannot contain what no longer exists. Dumps
land only inside the opened encrypted process volume; quiesce proves the
tuple's mounts, flushes, and reports the capsule digest; the host then
destroys the donor before taking snapshot evidence.

Related: [architecture](postflight-architecture.md) ·
[fleet](postflight-fleet.md) · [security model](postflight-security-model.md) ·
[storage](postflight-storage.md) ·
[runner lifecycle](postflight-runner-lifecycle.md)
