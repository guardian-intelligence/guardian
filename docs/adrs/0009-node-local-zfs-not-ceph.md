# 0009 — VM substrate storage is node-local ZFS, not Ceph

Status: Accepted · Date: 2026-07-11

## Context

The VM substrate (QEMU warm-pool CI runners now, interactive VMs later) needs
copy-on-write block storage for golden images, per-lease clones, and durable
caches. The incumbent pattern among CI vendors is a distributed block store
(Blacksmith runs Ceph RBD "sticky disks"). Workers are shared-nothing hosts by
prior ruling (single-node Talos, never members of the management cluster).

## Decision

Worker storage is ZFS on local NVMe. Ceph (and every networked block store) is
rejected. Scope: the worker substrate only — the management cluster's
DRBD-replicated LINSTOR tier is a separate decision serving data that must
survive.

- **Shared-nothing workers make replication a non-goal.** Golden images and
  durable caches are rebuildable, node-local performance state —
  never data of record; losing a disk costs one cold build. Ceph exists to
  replicate data that must survive; operating a second distributed system
  (mon/OSD quorum, its own failure modes and pager load) to protect data we
  deliberately do not back up inverts the design.
- **ZFS primitives map 1:1 onto the substrate's contract.** OpenZFS native
  encryption gives tenant isolation at rest — wrapping keys scoped per tenant
  and workspace lineage, once the TEE tier lands; pre-TEE zvols are plaintext
  by declared scope. `clone → promote → @sealed` gives sticky-disk semantics
  with atomic generation promotion; snapshot GUIDs give the content lineage
  that snapshot ABI pinning keys on; dataset `quota` gives the tenant storage
  boundary. All with zero network hops on the lease hot path: measured on
  guardian-w1, clone ~30ms and hot-attach to a pre-booted VM ~87ms, versus ~3s
  for Ceph-backed sticky-disk attach at the closest competitor.
- **Re-entry condition:** Ceph (or NVMe-oF) becomes worth revisiting only if
  cross-node mobility of caches/goldens becomes a product requirement that
  org→worker affinity routing cannot satisfy. Until then, placement is a
  control-plane decision and data-plane replication stays out of the system.

Related source: `docs/postflight-runner-designs/04-workspace-generations.md`,
`docs/postflight-runner-designs/README.md`
