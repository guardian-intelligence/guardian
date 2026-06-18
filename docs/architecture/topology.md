# Fleet topology

Ratified direction (binding summary: AGENTS.md "Compute doctrine"). This doc
owns the cluster shapes and the migration gates.

## Fleet truth

Five boxes, not six: `ash-bm-001` is the current Guardian dev host. All
Latitude ASH, f4.metal.small, one routed /31 each. Verself gamma
(206.223.228.87) and Verself prod (206.223.228.99) still run Verself under
Nomad; subsumption is ratified, but each box's wipe waits on an explicit
per-box operator go (AGENTS.md). Active Guardian hosts are named by stable
asset ID in `src/hosts` and by current assignment hostname in Talos.

## Target shape: two clusters, not five sites

- **prod: 3 nodes.** etcd quorum; rolling node upgrades replace wipe
  windows; OpenBao raft HA; the corpus moves to CloudNativePG synchronous
  replication (the single-copy-NVMe risk dies architecturally, not by
  backup cadence); replicas spread across machines.
- **nonprod: 2 nodes.** dev and gamma become namespaces (vCluster only if
  env-private API servers are ever earned). Two nodes rehearse everything
  multi-node except etcd quorum loss: scheduling, drains, rolling node
  upgrades, CNPG replication, BGP failover.
- **Why not one big cluster: the platform itself needs a staging
  environment.** App releases keep three gates (dev ns → gamma ns → prod
  cluster); platform releases (Talos/Cilium/k8s bumps) get two (nonprod
  cluster → prod cluster). Pooling must never cost the release discipline.

## Networking facts that bind the design

- The /31s are routed point-to-point — **no shared L2 between boxes**, so
  Cilium L2-announcement failover is impossible here. Multi-node ingress is
  **local BGP** (private ASN, Latitude per-project BGP sessions; floating
  service IPs, ECMP) or DNS multi-A. Verify Latitude BGP session mechanics
  before the prod migration; this is also why prod's edge conversion waits
  (`docs/architecture/gateway.md`).
- One metro for the whole fleet: etcd quorum is sound; anycast buys nothing
  until multi-metro.

## Workload plane

Per the compute doctrine: customer/untrusted work runs in QEMU microVMs
from per-host warm pools owned by the workload-agent binary — never as k8s
pods, so the clusters' threat model stays platform-components-only.
Kubernetes schedules the *agent* (tainted nodes, `/dev/kvm`); sandbox
placement is verself-v2 control-plane logic: power-of-two-choices over
eventually-consistent capacity heartbeats, lock-free, stale-tolerant. Warm
VMs are generic; the rootfs is late-bound at launch (lazy-pulled images) so
placement never develops locality. `quiesce` drains a host's pool without
disturbing running work; agent releases gate via load tests across the
nonprod fleet. Spare dev/staging capacity runs agents too — it IS the
load-test fleet.

## Migration gates, in order

1. Gateway dev pilot — topology-invariant, proceeds now.
2. Per-box operator go frees `vs-gamma-w0`/`vs-prod-w0` from Verself.
3. Grow `guardian-prod` 1→3 by joining freed boxes (no prod wipe); fold
   dev+gamma into the 2-node nonprod cluster.
4. Prod edge conversion (Gateway + BGP ingress) lands inside step 3 — never
   as separate prod surgery before it.
