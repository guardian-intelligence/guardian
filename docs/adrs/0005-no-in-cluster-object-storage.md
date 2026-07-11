# 0005 — No in-cluster object storage; R2 is the object tier

Status: Accepted · Date: 2026-07-07

## Context

Backups (Postgres barman, ClickHouse, etcd/Talos state) and any future S3-shaped
need require an object store. Running one in-cluster (SeaweedFS was the candidate)
prices poorly and reasons worse: the cluster's NVMe is too expensive to burn as an
S3 tier, a replicated ClickHouse on DRBD-backed object storage amplifies to ~9x raw
disk, and — decisively — an in-cluster backup target is circular: it dies with the
cluster it exists to resurrect.

## Decision

All object storage is Cloudflare R2, one bucket per consumer. SeaweedFS is out.

Rejected alongside, with their re-entry conditions:

- **Velero** — on a GitOps cluster the objects re-derive from Git and the databases
  need their own tools (barman, clickhouse-backup) regardless; Velero adds a second
  backup authority without covering the hard cases. Revisit if KubeVirt VM disks
  appear; `backup-audit.md` reserves a `velero/` prefix in the shared bucket.
- **OpenBao Transit for backup encryption** — a transit key born in-cluster dies
  with the raft it would be needed to restore. A restore must require only Git, the
  dark bundle, and custody. barman also has no client-side-encryption hook.
- **Operator-held age key** — viable and SOC2-compatible, but it cannot wrap the
  barman PITR stream, and key loss equals backup loss, so it demands seal-key-grade
  escrow. Deferred unless a compliance requirement names client-side encryption;
  SOC2 audits key management, not the tool.

## Consequences

- Restore paths depend on Cloudflare reachability; the dark bundle remains the
  offline distribution path (ADR 0008), not R2.
- R2 is not S3: no bucket versioning (state backups are copy-before-apply), and
  AWS-SDK CRC32 checksums break against it — clients need the checksum knobs
  turned off (`checksumAlgorithm: ""`, `AWS_REQUEST_CHECKSUM_CALCULATION=when_required`).
- Backup encryption is server-side at rest plus access control, until a compliance
  driver forces the escrowed client-side key.
