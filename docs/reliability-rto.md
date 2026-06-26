# Reliability RTOs

| Failure class | Target | Verification |
| --- | --- | --- |
| Single management node failure | Public edge outage under 60 seconds | Scheduled edge failover drill, one node at a time |
| Multiple management node failures | Restore service within 7 calendar days | Disaster recovery drill and backup restore evidence |

Single-node failover drills are not normal CI gates. They should run regularly,
rotate across all management nodes, and run after material changes to edge, DNS,
ingress, Talos, Cozystack platform, Cilium, or node networking.
