# 0011 — TigerBeetle uses three production replicas

Status: Accepted · Date: 2026-07-17

## Context

TigerBeetle is Guardian's financial system of record. Its cluster will contain
customer transactions and balances, not only synthetic data. TigerBeetle
recommends six replicas on six machines for a production cluster and three
sites for mission-critical geographic availability.

Guardian has exactly three management machines in one Latitude Ashburn site
and cannot provision six production failure domains. Deferring TigerBeetle
until six machines exist would leave the product without its selected ledger,
while placing six logical replicas on three machines would misrepresent the
failure domains and provide no additional machine-level fault tolerance.
TigerBeetle fixes the replica count when its data files are formatted, so this
choice must be explicit before cluster initialization.

## Decision

Guardian runs one three-replica production TigerBeetle cluster:

- replica 0 is fixed to `ash-earth`;
- replica 1 is fixed to `ash-wind`;
- replica 2 is fixed to `ash-water`;
- each replica owns one `local-encrypted-retain` LINSTOR volume on its node;
- TigerBeetle, rather than DRBD, replicates the three data files; and
- the cluster is allowed to hold customer transactions and balances after its
  readiness gates pass.

The operating failure budget is one unavailable node or replica. Every Talos,
firmware, Kubernetes, storage, and TigerBeetle operation is serialized: a
replica must be normal, synchronized, and serving before another node is
touched. An empty replacement data file always fails closed and is repaired
with `tigerbeetle recover`; it is never automatically formatted.

The single-site and three-machine risks are accepted and compensated with a
private ledger gateway, an off-site immutable recovery journal, continuous
reconciliation, aggressive replica health alerting, and demonstrated
single-replica and full-cluster recovery. These controls do not turn the three
machines into six failure domains. They make the chosen risk observable and
recoverable.

## Consequences

- One permanent node or disk loss is recoverable from the two-replica healthy
  cluster without losing acknowledged transactions.
- Two unavailable replicas stop safe transaction processing. A site-wide loss
  is a full disaster-recovery event, not automatic failover.
- There is no rack, site, region, or provider fault tolerance inside the
  TigerBeetle cluster.
- Increasing the replica count requires creating a new cluster and migrating
  the ledger. Replicas are not added to this cluster in place.
- A generic rescheduling controller must not move a replica onto another
  node, create a new empty data file, or hide loss of the node-local volume.
- Guardian must prove recovery from the off-site journal before admitting
  customer writes. Ordinary Kubernetes or R2 backup health is not evidence of
  TigerBeetle recoverability.

Related source: `docs/tigerbeetle.md`,
`src/infrastructure/base/storage/storageclasses.yaml`
