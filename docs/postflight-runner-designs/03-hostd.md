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
- Wires: sync loop → lease executor (`agent/lease.go`) → vm driver +
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

## Lease execution path (composition, mostly existing)

```
sync: listener lease allocating
  → claim an already-running warm VM (pool.go)
  → prepare over vsock (JIT config and pool env; no customer data)
  → generic runner registered and listening
  → observe GitHub's actual job-to-runner assignment
  → join that assignment to the selected listener lease
  → synchronous job-start hook reports and blocks on the same identity
  → resolve the actual execution lease's immutable generation tuple
  → clone its workspace zvol (04; empty if no generation exists)
  → QMP hot-attach every resolved zvol to that same QEMU
  → rendezvous over vsock (execution lease, job env, mounts) [01]
  → guest mounts, samples clock, and releases the hook
  → runner-status stream → listener/execution reports
  → on exit 0: quiesce → zfs snapshot (seal candidate)      [04]
  → destroy VM, release slot, refill pool
```

The listener lease identifies the physical VM and runner name. The execution
lease identifies the job, workspace, and completion being run there. These
are deliberately distinct: GitHub may assign two concurrent same-label jobs
to each other's JIT listeners. hostd reacts to the observed runner name and
routes the actual execution lease to that listener instead of predicting
which registration GitHub will choose. The control plane accepts the route
only for a live internal listener of the same runner class and preserves
capacity accounting for displaced jobs.

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
- The existing e2e (real agent + real PG + fake drivers) gains a variant
  with the real vsock transport against a guestd process on a local vsock —
  no QEMU needed (vsock loopback), which keeps it hermetic.
- Hardware conformance on the tracer host extends the merged 4 cases with:
  full lease path against a real VM, hostd kill/restart mid-lease
  (adoption), quiesce+seal.
