# 03 — hostd main, systemd launcher, vsock transport

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

Everything that turns the merged packages (`hostd/agent`, `hostd/vm`,
`hostd/zvol`, `hostd/checkoutbundle`, `syncproto`) into one daemon on the
tracer host. Most of the logic exists; this is wiring plus two real
implementations that today only have fakes.

## `cmd/hostd`

Env-configured main (mirroring the controlplane's config style):

- `HOSTD_HOST_ID`, `HOSTD_SYNC_URL`, `HOSTD_SYNC_SECRET` — sync loop against
  the controlplane (merged client in `hostd/agent`).
- `HOSTD_STATE_DIR` — the merged vm state-dir driver (adoption-from-disk on
  restart is already conformance-tested; hostd restart under load is a
  hammer scenario, not a new feature).
- `HOSTD_POOL` (`tank/postflight`), `HOSTD_SLOTS` (~4–8 on the tracer box),
  `HOSTD_CLASS` (`postflight-4cpu-ubuntu-2404`), `HOSTD_IMAGE_ID` — pool
  sizing and the image template to clone root disks from (02).
- Wires: sync loop → pool-member and assignment convergence → VM driver +
  zvol manager → guest transport. One process, systemd service on the host
  (`Restart=on-failure` — hostd itself may restart; VMs must survive it).

## systemd-run launcher (new `vm.Launcher` impl)

The pod launcher needs Talos; the tracer host is plain Ubuntu. Same
supervision properties, different cage:

- `Launch`: `systemd-run --scope --unit=pf-vm-<id> --collect` around the
  golden argv. A scope (not a service) because QEMU must be *our* child for
  QMP fd handling to stay simple, while living in its own cgroup with
  independent lifetime — hostd restart never kills VMs.
- No auto-restart anywhere: a dead QEMU is a dead slot, collected and
  refilled. Resurrection violates destroy-and-refill.
- `Alive`/`Kill`: cgroup existence check; kill = QMP quit, then scope stop
  after the grace window (mirrors the pod launcher's poll-gone contract).
- Adoption: on restart, state-dir metas name their scope units; probe QMP
  socket + cgroup, quarantine on mismatch (merged semantics, new probe).

## vsock guest transport (replaces the fake)

- Host side dials `AF_VSOCK` (CID from the VM record, port 1) with connect
  timeout; `guestproto` JSON-lines over the socket.
- Probe = dial + `hello` within deadline. Used by the boot ladder
  (booting→warm) and by adoption.
- Retry policy: dial failures during boot are expected (guest still coming
  up) — the boot deadline owns the ladder; no infinite retries.

## Durable assignment path

```
sync: generic VM is warm
  → allocate a durable pool-member incarnation
  → prepare over vsock (single-use JIT config; no customer data)
  → generic runner registered and continuously listening
  → Runner.Listener receives GitHub's encrypted job message
  → publish check-run/request/protocol-job identity before Worker dispatch
  → create one immutable job-to-member assignment in the control plane
  → clone its workspace/tool generation and its process image when valid
  → otherwise attach an empty process zvol for a cold capsule
  → QMP hot-attach that tuple to the already selected QEMU
  → guest mounts and attempts the process restore
  → recoverable restore miss tears out process state and cold-starts
  → integrity or unprovable-cleanup failure destroys the VM fail-closed
  → publish provider withdrawal/fail-closed only after VM destruction is proven
  → authorize the blocked Worker only after exact identity validation
  → on exit 0: checkpoint and flush, destroy QEMU, seal all zvols
  → refill the pool with a new member incarnation
```

The member identifies one physical VM incarnation and runner name. The
assignment identifies one GitHub job and its durable generation. GitHub first
selects the member; the local Runner.Listener observation tells hostd which
member was selected. The check-run ID joins exactly to provider truth, so
concurrent jobs with identical labels and display names cannot cross-bind.

The checkout-bundle server (`checkoutbundle`, merged) is wired in as-is:
it serves repo bundles on the host-local network for the checkout action's
delta fetch.

## Host provisioning (tracer box, one-time)

- Striped zpool `tank` exists; cap ZFS ARC (`zfs_arc_max`) so ~4–8 slots of
  4 vCPU / 16 GiB fit alongside it.
- Install: hostd binary + systemd unit + `HOSTD_SYNC_SECRET` minted in the
  controlplane namespace and delivered once, by hand, for this pass
  (per-host sync creds are a recorded follow-up before host #2).

## Testing

- Unit: launcher against a stub argv (`sleep`) asserting scope lifecycle,
  adoption, kill-grace; transport against a vsock loopback where available,
  fake elsewhere.
- The e2e boots real PostgreSQL and the control-plane HTTP server, drives a
  real hostd agent over sync, and uses deterministic fake VM/ZFS drivers for
  failure injection. The vsock transport has a separate loopback suite.
- Hardware conformance on the tracer host extends the merged 4 cases with:
  full assignment path against a real VM, hostd kill/restart mid-assignment
  (adoption), quiesce+seal.
