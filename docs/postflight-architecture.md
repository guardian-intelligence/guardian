# Postflight production architecture

Status: current first-party Turbo implementation, 2026-09-06. Implementation
is not deployment evidence: the [CI migration proof contract](postflight-ci-migration.md)
records the required real jobs, cache lineage, VM restore, and log checks.
Confidential remains a separate security profile with its own release gates.

Companions: [fleet](postflight-fleet.md), [Lightning](postflight-lightning.md),
[security model](postflight-security-model.md), [scheduling](postflight-scheduling.md),
[storage](postflight-storage.md), [host](postflight-host.md), and
[runner lifecycle](postflight-runner-lifecycle.md).

## Runner profiles

| | First-party Turbo | Confidential |
| --- | --- | --- |
| Runner class | `postflight-4vcpu-ubuntu24-turbo` | Separate confidential classes, unchanged by Turbo rollout |
| Guest | Ubuntu 24.04, 4 vCPU, 16 GiB, QEMU/KVM | SEV-SNP guest; attestation and confidential-image requirements |
| TEE requirement | None | SEV-SNP |
| Boot reuse | Generic pre-registration QEMU RAM + root template | Turbo RAM template path is rejected |
| Cross-job cache | Scoped workspace and tool ZFS generations | Separate confidential encryption profile |
| At-rest keys | Native encrypted host ZFS; raw key seeded from OpenBao outside Git | In-guest SNP-derived volume keys |
| Host trust | Trusted Guardian host, including kernel, QEMU, hostd, and storage key | Untrusted host is the confidential security-policy target |

The concrete Turbo class is seeded by
[migration 012](../src/postflight/controlplane/migrations/012_turbo_runner_class.sql).
Its [host manifest](../src/postflight/host/hosts/rust-forge-01.json) declares two
slots, the exact installed QEMU version, SeaBIOS, and the encrypted dataset.
The [launch profile](../src/postflight/hostd/vm/spec.go) uses `pc-q35-8.2`, the
host CPU model, and non-root `postflight-vm`. These are host-local templates;
there is no portable CPU or cross-host memory-restore claim.

## The assembly

```text
GitHub workflow_job webhook/API -> control plane -> Postgres
                                      |
                         authenticated hostd sync
                                      |
                                    hostd
                         /            |            \
                 encrypted ZFS    QEMU + KVM    checkout broker
                                      |
                                    vsock
                                      |
                                   guestd
                                      |
                         patched Runner.Listener / Worker
                                      |
                         GitHub assignment and live job logs
```

The [control-plane scheduler](../src/postflight/controlplane/scheduler.go)
uses signed webhook hints and API reconciliation to admit class demand, create
pools, mint fresh GitHub App JIT registrations, and preposition plans.
GitHub chooses the listener; its observed assignment binds the selected VM
to the exact job before Worker starts. GitHub remains the workflow engine.
The patched [Actions runner](../src/postflight/runner/runner-listener.patch) uses GitHub's
normal job/log protocol. Postflight host and guest lifecycle events are a
separate operational stream, not a replacement job-log uploader.

The current [host sync protocol](../src/postflight/hostd/syncproto) is bounded
JSON over authenticated HTTPS; guest frames use the
[vsock protocol](../src/postflight/hostd/guestproto). The older two-gRPC-stream
and single-protobuf design is an end-state proposal, not the running wire
contract. [Operator status](../src/postflight/controlplane/hostd_status.go)
projects host health, pools, demands, assignments, and generation lineage
without JIT credentials, raw webhook bodies, or tenant tokens.

## Two independent kinds of reuse

**Boot reuse is generic and secretless.** Before its scheduling agent starts,
hostd boots a donor that has never been registered, assigned, or given tenant
volumes. It pauses QEMU, saves RAM/device state, destroys the donor, and seals
the matching root zvol. The [template implementation](../src/postflight/hostd/vm/warm_template.go)
binds the host boot, image, QEMU and firmware bytes, machine/CPU model,
network mode, and VM geometry. A restored guest gets fresh entropy, clock,
machine identity, MAC and DHCP state before the listener receives JIT
registration; see [guest initialization](../src/postflight/guestd/initialize_linux.go).
Memory lives under `/var/lib/postflight/warm-templates` on encrypted ZFS,
separate from the ZFS `templates` dataset's mountpoint.

**Cross-job reuse contains disk state.** The assignment materializes scoped
workspace and tool clones. On completion, the guest flushes the durable
filesystems and hostd destroys QEMU before sealing disk snapshots. Only an
eligible trusted attempt with API-confirmed success may promote a candidate
using a scope-pointer compare-and-swap. A subsequent VM can reuse those disks
while starting fresh customer processes.

Arbitrary customer-process CRIU publication and restore remain disabled by
the security boundary introduced in PR #1212. The
[plan gate](../src/postflight/hostd/agent/plans.go),
[sync gate](../src/postflight/hostd/agent/sync.go), and
[completion path](../src/postflight/hostd/agent/converge.go) enforce that
boundary. Retained CRIU libraries and image tools do not imply enabled
customer-process restoration. A registered listener, Worker, job token,
tenant disk, or customer process can never become a generic template donor.

## Operational and trust boundaries

- One job per VM; completion, cancellation, loss, and unsafe initialization
  destroy and refill the guest. Hostd restarts adopt surviving VM scopes.
- Warm state is a regenerable local cache. A new image or host boot produces
  a new RAM template; incompatible or absent disk state costs a cold build.
  No customer memory replication, cross-host generation transport, or durable
  customer-state backup is provided by this path.
- Turbo uses the baked `host-zfs` guest profile, not a runtime downgrade of a
  confidential image. Hostd verifies every managed dataset is encrypted and
  its key loaded before serving. The trusted host can read guest state and
  mounted disk plaintext. The root-only ZFS key is also on the host; native
  encryption does not protect a stolen complete host filesystem containing
  that key, nor provide per-tenant crypto-erase.
- [Host reconciliation](../src/postflight/host/README.md) follows protected
  public main from root-owned source and build locations. Secrets remain
  outside Git. Guest networking permits public egress and only DHCP, DNS,
  and checkout access to the host; private/reserved destinations, IPv6, and
  guest-to-guest forwarding are denied.
- Confidential attestation, key custody, and untrusted-host release gates are
  preserved. Turbo's successful CI run does not establish those claims.
- Report measured Linux evidence separately for VM boot restore, disk cache
  reuse, GitHub log streaming, and end-to-end job latency. Historical tracer
  timings and fake-backed tests do not establish the current host's speed.
