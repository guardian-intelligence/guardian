# 0009 — VM substrate storage is node-local ZFS, not Ceph

Status: Accepted · Date: 2026-07-11

## Context

The VM substrate (Firecracker CI runners now, interactive VMs later) needs
copy-on-write block storage for golden images, per-lease clones, and durable
caches. The incumbent pattern among CI vendors is a distributed block store
(Blacksmith runs Ceph RBD "sticky disks"). Workers are shared-nothing plain
Ubuntu hosts by prior ruling.

## Decision

Worker storage is ZFS on local NVMe. Ceph (and every networked block store) is
rejected.

- **Shared-nothing workers make replication a non-goal.** Golden images and
  durable caches are rebuildable, encrypted, node-local performance state —
  never data of record; losing a disk costs one cold build. Ceph exists to
  replicate data that must survive; operating a second distributed system
  (mon/OSD quorum, its own failure modes and pager load) to protect data we
  deliberately do not back up inverts the design.
- **ZFS primitives map 1:1 onto the substrate's contract.** Per-org encryption
  roots give tenant isolation at rest; `clone → promote → @sealed` gives
  sticky-disk semantics with atomic generation promotion; snapshot GUIDs give
  the content lineage that snapshot ABI pinning keys on; `quota` on the org
  root is the tenant storage boundary. All with zero network hops on the lease
  hot path: measured on guardian-w1, clone ~30ms and warm restore ~520ms,
  versus ~3s for Ceph-backed sticky-disk attach at the closest competitor.
- **Re-entry condition:** Ceph (or NVMe-oF) becomes worth revisiting only if
  cross-node mobility of caches/goldens becomes a product requirement that
  org→worker affinity routing cannot satisfy. Until then, placement is a
  control-plane decision and data-plane replication stays out of the system.
