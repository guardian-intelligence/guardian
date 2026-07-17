# TigerBeetle production contract

TigerBeetle is Guardian's financial system of record for customer
transactions and balances. It works alongside CNPG: TigerBeetle owns accounts,
transfers, balance constraints, and transaction ordering; Postgres owns
descriptions, user-facing metadata, and mappings to product entities.

The deployment is one production cluster with three replicas. It is not a
synthetic-only qualification cluster. [ADR 0011](adrs/0011-three-replica-tigerbeetle.md)
records the accepted machine and site failure-domain constraint.

## Fixed topology

The cluster contract is:

| Replica | Node | Private address | Data class |
| ---: | --- | --- | --- |
| 0 | `ash-earth` | `10.8.0.11:3000` | `local-encrypted-retain` |
| 1 | `ash-wind` | `10.8.0.12:3000` | `local-encrypted-retain` |
| 2 | `ash-water` | `10.8.0.13:3000` | `local-encrypted-retain` |

The ordered address list, a random nonzero 128-bit cluster ID, replica count
`3`, and replica indices become immutable when the data files are formatted.
The cluster ID is an identifier, not a secret, and belongs in the declared
deployment after initialization.

Each replica has required node affinity and required pod anti-affinity. It
runs on the host network so the address list is independent of pod identity.
The three data files use separate node-local LINSTOR allocations. They do not
use `replicated-encrypted`: DRBD underneath TigerBeetle would duplicate
replication, multiply writes, and collapse the distinction between
TigerBeetle's independent replica files.

The volume remains encrypted twice: the LINSTOR allocation uses its native
LUKS layer and the backing NVMe pool is inside Talos LUKS2. `Retain` prevents
StatefulSet or PVC deletion from deleting the financial data file.

## Failure contract

Exactly one replica may be unavailable. Two unavailable replicas mean the
ledger is unavailable and must remain unavailable rather than accept an
unsafe write.

Every planned operation follows this sequence:

1. confirm all three replicas report normal status and no state sync;
2. stop or drain exactly one replica;
3. perform the node, storage, or binary operation;
4. wait for that replica to return to normal and complete state sync;
5. run a committed-write/read canary and reconciliation check; and
6. only then permit work on another node.

The PodDisruptionBudget allows at most one unavailable replica. Automated
rollouts are ordered one replica at a time. Node drains, Talos upgrades,
firmware changes, Secure Boot database changes, and TigerBeetle upgrades all
share the same disruption budget.

A missing or empty steady-state volume is never initialized. The replica
fails closed and pages. Replacement uses `tigerbeetle recover` against the
healthy cluster, followed by state sync and balance reconciliation. The
steady-state workload contains no `tigerbeetle format` path.

## Security boundary

TigerBeetle has no authentication layer. It has no public Service, public DNS
record, ingress, or direct product-client path.

A stateless ledger gateway is the only non-replica workload allowed to reach
TCP port 3000. The gateway:

- authenticates the calling workload or user and enforces authorization;
- validates ledger, account, transfer, amount, and code invariants;
- generates or accepts stable 128-bit idempotency identifiers;
- batches requests while preserving per-request results;
- emits an actor- and request-correlated audit record; and
- exposes no TigerBeetle data-file or administration operation.

Cilium policy defaults product callers to deny and permits only approved
workloads to reach the gateway. The host-network replicas are not assumed to
be covered by pod-level Cilium policy. A ValidatingAdmissionPolicy confines
host networking to the exact digest-pinned TigerBeetle and gateway workloads,
their dedicated service accounts, and their namespace. Talos host firewall
policy permits replica TCP port 3000 only between the three declared
management-node identities; the host-network gateway mediates product access.
Replica and gateway traffic must use an encrypted node-to-node path; private
addressing alone is not transport encryption.

No personal data belongs in TigerBeetle's immutable fields. `user_data`
contains opaque identifiers pointing to access-controlled metadata in
Postgres. Ledger IDs, account codes, transfer codes, asset scales, and
correction codes are append-only registries reviewed as source.

## Off-site recovery journal

Three replicas in one site protect against one node or disk loss, not loss of
the site. The generic cluster backup does not make TigerBeetle recoverable.

Before submitting an account or transfer mutation, the gateway durably writes
the exact immutable command and idempotency ID to an off-site recovery journal.
After TigerBeetle replies, it appends the result and assigned timestamp. The
caller receives success only after the outcome is durable off-site. A timeout
after TigerBeetle may have committed is resolved by retrying the same
idempotency ID, never by inventing another transfer.

Journal records:

- are append-only and encrypted in the R2 backup boundary;
- contain opaque identifiers rather than personal data;
- distinguish production and synthetic ledger IDs;
- preserve the exact account or transfer body, result, and TigerBeetle
  timestamp;
- are continuously reconciled against TigerBeetle; and
- are retained according to the financial-record retention policy.

The restore tool builds an empty replacement cluster from the journal in
TigerBeetle timestamp order, then proves account balances and transfer counts
against signed reconciliation checkpoints. This path is not considered valid
until it succeeds in an isolated full-cluster restore drill using the pinned
server and client release.

## Runtime and capacity gates

Before formatting the production data files:

1. Pin the same TigerBeetle server and client release by digest/version,
   mirror the server image into zot, include it in the dark bundle, and apply
   the existing signature and provenance controls.
2. Verify ECC memory from node hardware evidence.
3. Prove `io_uring`, direct I/O, and memory locking on an encrypted local
   volume with the intended container runtime.
4. Grant only `IPC_LOCK` and the minimum seccomp exception demonstrated by
   the compatibility test. Do not grant a privileged container or raw device
   access.
5. Measure current node headroom and reserve a full CPU core plus the chosen
   cache and process memory on every node. Scheduling must remain safe when
   ordinary management workloads resettle after one node loss.
6. Forecast transfer growth, allocate the initial PVC size, and alert before
   either the filesystem or LINSTOR pool loses its recovery reserve.
7. Benchmark the double-encrypted ext4 path under expected and burst traffic.
   Cache size, PVC size, and resource requests come from this evidence.

Formatting is a distinct, reviewable bootstrap deployment. It verifies that
all three target volumes are empty, formats exactly one replica index per
node, and is removed from the steady-state manifests after the cluster is
healthy. Reconciliation must never be able to recreate a format job.

## Observability and canaries

Each replica emits TigerBeetle StatsD metrics to a local bridge scraped by
VictoriaMetrics. Alert at minimum on:

- any non-normal replica status;
- any state-sync stage;
- fewer than three reachable replicas;
- process restart or crash loop;
- disk and LINSTOR pool capacity;
- NTP synchronization;
- memory-lock or direct-I/O startup failure;
- request latency and error-rate regression; and
- cache misses indicating an undersized cache.

A continuously running canary uses dedicated synthetic ledger, account, and
transfer codes. It creates, reads, retries, posts, voids, and reverses bounded
transactions and verifies that balances and idempotency results match. Its
records remain visibly synthetic even though the cluster also contains
production data.

The gateway publishes end-to-end latency, result codes, journal latency,
journal/TigerBeetle reconciliation lag, and caller identity. Alerts must
distinguish database unavailability, authorization denial, invalid financial
input, and off-site journal failure.

## Readiness sequence

Customer writes are admitted only after these steps complete in order:

1. **Financial model:** approve asset scales, ledger IDs, account and transfer
   codes, balance constraints, pending-transfer lifecycle, correction
   semantics, and Stripe reconciliation.
2. **Compatibility:** pass the encrypted-volume `io_uring`, direct-I/O,
   memlock, ECC, capacity, and network-encryption checks on all three nodes.
3. **Supply chain:** pin and verify the server image and matching client,
   mirror them into the dark bundle, and test an offline start.
4. **Runtime:** deploy fixed-address replicas, local retained PVCs, disruption
   controls, host firewall rules, and fail-closed bootstrap behavior.
5. **Gateway:** deploy authentication, authorization, idempotency, batching,
   immutable code registries, default-deny Cilium policy, the scoped
   host-network admission rule, and Talos host firewall policy.
6. **Recovery journal:** make intent and outcome records durable off-site and
   continuously reconcile them with TigerBeetle.
7. **Observability:** ship replica, host, storage, gateway, and journal metrics;
   prove every critical alert reaches the pager.
8. **Functional proof:** exercise account creation, linked transfers,
   pending/post/void, retries after lost replies, corrections, and invariant
   failures.
9. **Failure proof:** independently stop each replica, partition it, restart
   it, and verify zero loss of acknowledged transactions before proceeding to
   the next node.
10. **Disk-loss proof:** destroy one non-primary test data file, recover it
    with `tigerbeetle recover`, complete state sync, and reconcile every
    balance.
11. **Full-DR proof:** rebuild all three replicas in isolation from off-site
    evidence and meet the declared RPO/RTO.
12. **Performance proof:** run expected load, burst load, one-replica-down
    load, and a sustained soak without violating latency, capacity, or
    reconciliation thresholds.
13. **Release:** enable customer writes behind a fail-closed feature flag,
    watch canary and reconciliation signals, and retain an immediate ability
    to stop new writes without modifying historical transactions.

References:

- [TigerBeetle cluster recommendations](https://docs.tigerbeetle.com/operating/cluster/)
- [TigerBeetle hardware](https://docs.tigerbeetle.com/operating/hardware/)
- [TigerBeetle deployment](https://docs.tigerbeetle.com/operating/deploying/)
- [TigerBeetle recovery](https://docs.tigerbeetle.com/operating/recovering/)
- [TigerBeetle monitoring](https://docs.tigerbeetle.com/operating/monitoring/)
- [TigerBeetle system architecture](https://docs.tigerbeetle.com/coding/system-architecture/)
- [Reliable transaction submission](https://docs.tigerbeetle.com/coding/reliable-transaction-submission/)
