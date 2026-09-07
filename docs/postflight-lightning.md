# Lightning

Status: current Turbo warmth mechanisms, 2026-09-06.

Lightning names Postflight's reusable CI state, not a separate SKU. The
current `postflight-4vcpu-ubuntu24-turbo` path combines a generic VM boot
template with scoped ZFS disk caches. Confidential retains its separate
security profile; generic QEMU RAM restoration is admitted only for Turbo.
See the [architecture](postflight-architecture.md) for code and trust boundaries.

## Boot the platform once, register each runner fresh

Hostd creates a generic QEMU RAM/device-state image with a matching root
snapshot before its scheduler starts. The donor has never received GitHub
registration, an assignment, or tenant storage. Each restored VM gets a new
identity, entropy, clock, MAC, and DHCP lease before a fresh Listener starts.
The template is tied to the host boot and exact runtime inputs; a customer
job never supplies its memory. See
[warm_template.go](../src/postflight/hostd/vm/warm_template.go) and
[initialize_linux.go](../src/postflight/guestd/initialize_linux.go).

This reduces platform boot work. It is separate from the pool of registered
listeners awaiting GitHub assignment. A registered listener is single-use:
one job, then destroy and refill.

## Preserve useful disk work across jobs

Workspace and tool state live in encrypted host-local zvols. The selected
assignment clones a compatible scoped generation and hot-attaches its disks
before Worker starts. A trusted successful attempt can publish the next
cache generation only after the guest flushes the filesystems, hostd destroys
the VM and seals the disks, and the control plane verifies the exact GitHub
attempt's success. Untrusted writes do not become trusted cache heads.
See [storage](postflight-storage.md) and the
[Turbo end-to-end policy test](../src/postflight/controlplane/turbo_e2e_test.go).

The initial host uses a 384-GiB file-backed ZFS pool on local storage. Its
managed child has native AES-256-GCM encryption, with the key seeded from
OpenBao outside Git. The guest uses the baked `host-zfs` profile. Turbo
trusts the host; this does not promise plaintext exclusion from host RAM or
per-tenant cryptographic erasure.

## Customer process memory stays fresh

Arbitrary build-process CRIU publication and restoration remain disabled.
Compiler daemons, watchers, JIT state, runner registration, and job tokens
are not restored from previous jobs. Disk warmth can still make later builds
incremental. The retained capsule/CRIU libraries are not an enabled runtime
feature; [agent plan validation](../src/postflight/hostd/agent/plans.go)
rejects process-restore requests.

A cache miss or compatibility change costs a cold build. Host loss does not
require restoring customer cache data from a backup. There is no cross-host
warm-state transport in this rollout.

## Evidence before speed claims

The older guardian-w1 tracer measurements dated 2026-07-05 (~520 ms full
restore, eight restores in 774 ms, and 227 ms hot-attach) describe that tracer
and hardware only. They are not current Turbo CI latency or production SLA
measurements. The [CI migration proof contract](postflight-ci-migration.md)
requires real Linux VM restores with unique concurrent guest identities,
cross-VM generation lineage, native GitHub logs, and actual job results.

Related: [architecture](postflight-architecture.md),
[runner lifecycle](postflight-runner-lifecycle.md),
[host provisioning](../src/postflight/host/README.md).
