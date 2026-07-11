# Reliability RTOs

| Failure class | Target | Verification |
| --- | --- | --- |
| Single management node failure | Public edge outage under 60 seconds | [`edge-failover-drill`](../src/infrastructure/runbooks/edge-failover-drill.md), ~2×/48h, rotating across all management nodes, and after material changes to edge, DNS, ingress, Talos, Cozystack platform, Cilium, or node networking |
| Multiple management node failures | Restore service within 7 calendar days | Full DR drill per [`cold-boot-bootstrap`](../src/infrastructure/runbooks/cold-boot-bootstrap.md), monthly and after material changes to etcd, Talos, Cozystack, backup tooling, or custody; interim evidence from [`backup-audit`](../src/infrastructure/runbooks/backup-audit.md), [`etcd-snapshot-restore`](../src/infrastructure/runbooks/etcd-snapshot-restore.md), and [`wiped-node-drill`](../src/infrastructure/runbooks/wiped-node-drill.md) |
