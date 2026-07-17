# TigerBeetle production contract

TigerBeetle is Guardian's financial system of record for customer
transactions and balances. It works alongside CNPG: TigerBeetle owns accounts,
transfers, balance constraints, and transaction ordering; Postgres owns
descriptions, user-facing metadata, and mappings to product entities.
[The financial model](tigerbeetle-financial-model.md) fixes the production
asset scale, numeric registry, credit lifecycle, correction semantics, and
payment-processor reconciliation contract.

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

The data files use cluster ID
`49532141921164377784457307205600684260`, replica count `3`, and replica
indices `0`, `1`, and `2`. The cluster ID is an identifier, not a secret.
All three replicas run TigerBeetle `0.17.9` from the same digest-pinned OCI
index.

Each replica is a separate `Recreate` Deployment with a required node
selector and a dedicated retained PVC. It runs on the host network so the
address list is independent of pod identity. The three data files use separate
node-local LINSTOR allocations. They do not use `replicated-encrypted`: DRBD
underneath TigerBeetle would duplicate replication, multiply writes, and
collapse the distinction between TigerBeetle's independent replica files.

The volume remains encrypted twice: the LINSTOR allocation uses its native
LUKS layer and the backing NVMe pool is inside Talos LUKS2. `Retain` prevents
workload or PVC deletion from deleting the financial data file.

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

Cilium policy defaults product callers to deny and permits only the payments
gateway to reach node TCP port 3001. One digest-pinned Envoy process per node
requires a client certificate with the payments DNS SAN, verifies it against
the dedicated TigerBeetle transport CA, and forwards the accepted byte stream
over loopback to TCP port 3000. The gateway uses three loopback listeners so
the TigerBeetle client retains one independently authenticated connection per
replica. The client certificate is mounted only in the payments pods.

The host-network replicas and transport proxies are not assumed to be covered
by pod-level Cilium policy. A ValidatingAdmissionPolicy confines both
host-network exceptions to exact digest-pinned images, the dedicated
`tigerbeetle` service account, `tenant-guardian`, and the three management
nodes. Talos host firewall policy continues to permit the unencrypted replica
port 3000 only between the declared management-node identities; pod traffic
can reach only the mTLS port 3001.

The operational-test runtime uses the dedicated Latitude private VLAN. It has
no customer data or product-client path. Customer writes remain disabled until
the gateway and replica address plane use an encrypted node-to-node path;
private addressing and port isolation alone are not transport encryption.

No personal data belongs in TigerBeetle's immutable fields. `user_data`
contains opaque identifiers pointing to access-controlled metadata in
Postgres. Ledger IDs, account codes, transfer codes, asset scales, and
correction codes are the append-only registry in
[`tigerbeetle-financial-model.md`](tigerbeetle-financial-model.md).

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

## Runtime and capacity

Each replica has a 100 Gi local retained PVC, one exclusive requested CPU,
an 8 Gi memory request and limit, and a 4 Gi grid cache. The container runs as
non-root with a read-only root filesystem, no service-account token, only the
`IPC_LOCK` capability, and the seccomp exception required for `io_uring`. It
has no privileged mode, host path, or raw-device access.

The reconciled workload contains no `tigerbeetle format` command. Any missing
or empty data file therefore fails closed. Capacity alerts fire at 20% free
space; expected-load, burst, one-replica-down, and soak measurements determine
the first expansion and cache adjustment.

## Observability and canaries

Each replica emits TigerBeetle StatsD metrics to a local bridge scraped by
VictoriaMetrics. The current runtime pages or warns on:

- any non-normal replica status;
- a state-sync stage lasting more than 15 minutes;
- fewer than three reachable replicas;
- process restart;
- missing status metrics; and
- less than 20% free space on any replica PVC.

A continuously running canary uses the dedicated synthetic ledger and
synthetic accounts, with the same account and transfer code registry as
production. It creates, reads, retries, posts, voids, and reverses bounded
transactions and verifies that balances and idempotency results match. Its
records remain visibly synthetic even though the cluster also contains
production data.

The gateway publishes end-to-end latency, result codes, journal latency,
journal/TigerBeetle reconciliation lag, and caller identity. Alerts must
distinguish database unavailability, authorization denial, invalid financial
input, and off-site journal failure.

## Customer-write readiness

The financial model, image pin, encrypted retained volumes, fixed-node
runtime, disruption budget, and initial replica observability are present.
Customer writes remain disabled until these remaining gates complete in
order:

1. **Compatibility:** pass the encrypted-volume `io_uring`, direct-I/O,
   memlock, ECC, capacity, and network-encryption checks on all three nodes.
2. **Supply chain:** mirror the pinned server image and matching client into
   the dark bundle and test an offline start.
3. **Gateway:** deploy authentication, authorization, idempotency, batching,
   immutable code registries, default-deny Cilium policy, the scoped
   host-network admission rule, and Talos host firewall policy.
4. **Recovery journal:** make intent and outcome records durable off-site and
   continuously reconcile them with TigerBeetle.
5. **Observability:** ship host, LINSTOR-pool, gateway, journal, NTP,
   request-latency, error-rate, and cache-efficiency metrics;
   prove every critical alert reaches the pager.
6. **Functional proof:** exercise account creation, linked transfers,
   pending/post/void, retries after lost replies, corrections, and invariant
   failures.
7. **Failure proof:** independently stop each replica, partition it, restart
   it, and verify zero loss of acknowledged transactions before proceeding to
   the next node.
8. **Disk-loss proof:** destroy one non-primary test data file, recover it
    with `tigerbeetle recover`, complete state sync, and reconcile every
    balance.
9. **Full-DR proof:** rebuild all three replicas in isolation from off-site
    evidence and meet the declared RPO/RTO.
10. **Performance proof:** run expected load, burst load, one-replica-down
    load, and a sustained soak without violating latency, capacity, or
    reconciliation thresholds.
11. **Release:** enable customer writes behind a fail-closed feature flag,
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
- [Guardian financial model](tigerbeetle-financial-model.md)
