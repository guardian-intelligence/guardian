# Runbook: management cluster evidence

This runbook is the evidence path after `guardian-mgmt` is converged. It does
not replace `docs/runbooks/cozystack-mgmt-rebuild.md`; it starts after the
Talos VIP, Kubernetes API, Cozystack platform package, tenants, and base
manifests are applied.

The rules for this evidence are:

- use repo-pinned tools through `aspect`;
- write reports under `docs/reports/infrastructure/`;
- do not rely on `DELETE_ME.env` or any other file outside the repo as desired
  state;
- do not count preflight render/build checks as load, disaster-recovery, or
  outage evidence.

## Preflight

Run this before changing live infrastructure:

```sh
aspect infra preflight
aspect infra plan
aspect infra dns-plan
```

`infra preflight` validates both OpenTofu roots without opening their remote
backends, builds the company-site OCI artifact, and renders
`src/infrastructure/base` with the repo-pinned kubectl.

## Convergence Snapshot

Once the management kubeconfig exists, apply and snapshot the declared platform:

```sh
aspect infra apply-base --kubeconfig "${KUBECONFIG}"
aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"
aspect infra live-rollout --kubeconfig "${KUBECONFIG}"
```

For Talos and etcd health over the Layer2 VIP:

```sh
aspect infra talos-health \
  --talosconfig "${TALOSCONFIG}" \
  --endpoints 10.8.0.250 \
  --nodes 10.8.0.11,10.8.0.12,10.8.0.13
```

`infra live-snapshot` expects these resources to exist and be queryable:

- `apps.cozystack.io` app CRs in `tenant-root`: Harbor `oci`, ClickHouse
  `ledger`, Postgres `guardian`, and OpenBAO `guardian`;
- tenants `tenant-root`, `tenant-dev`, and `tenant-gamma`;
- R2 backup Secret contract `tenant-root/guardian-r2-db-backups` by name only;
- company-site Deployment/Service/Ingress in `tenant-dev`, `tenant-gamma`, and
  `tenant-root`;
- Cozystack backup resources (`BackupClass`, `Plan`, `BackupJob`, `Backup`);
- storage classes and PVC/PV state.

## Load Evidence

Each component report must state the exact command, target, concurrency, request
count or duration, observed error count, and success threshold.

Required component coverage:

- Talos / Kubernetes API VIP: repeated API reads through `10.8.0.250`, plus
  Talos health and etcd member checks.
- LINSTOR / DRBD: create PVCs from `replicated` and `replicated-retain`, write
  data, reschedule the workload, and verify the data survives.
- OpenBao: initialize/unseal for the test run, perform token-authenticated
  read/write load against a non-production path, then reseal/unseal.
- CNPG / Postgres: run SQL write/read load through the managed app, then verify
  replication status and failover posture.
- Harbor: push and pull a digest-addressed test artifact, then verify the digest
  matches.
- ClickHouse: write/query wide-event style rows and verify Keeper/replica
  health.
- Cozystack Dashboard: check HTTPS reachability and auth redirect behavior.
- Public ingress / DNS: check all managed names resolve to the declared
  management-node public IPs and serve the expected host.
- Company site dev/gamma/prod: load `/`, `/letters/`, `/news/`, `/healthz`, and
  `/metrics` on all three hosts.

## Disaster Recovery Evidence

Cozystack v1.4's managed database backup path uses admin-provisioned
`BackupClass` resources, tenant `BackupJob`/`Plan` objects, and `RestoreJob`
objects. Postgres also needs `backup.enabled=true` at chart install so CNPG WAL
archiving starts before the first backup; ClickHouse needs `backup.enabled=true`
so the Altinity sidecar exists.

The checked-in desired state declares:

- Postgres WAL archive plumbing in `src/infrastructure/base/apps/postgres.yaml`;
- ClickHouse backup sidecar plumbing in
  `src/infrastructure/base/apps/clickhouse.yaml`;
- backup strategies, `BackupClass` objects, and hourly `Plan` objects in
  `src/infrastructure/base/backups/managed-databases.yaml`;
- the non-secret R2 backup contract in
  `src/infrastructure/inventory/guardian-mgmt.json`.

The Secret `tenant-root/guardian-r2-db-backups` must exist before the Postgres
and ClickHouse releases can complete. Required keys are `bucketName`,
`endpoint`, `region`, `AWS_ACCESS_KEY_ID`, and `AWS_SECRET_ACCESS_KEY`.
Secret-zero seeding is responsible for minting it; once the OpenBao projection
controller exists, OpenBao should become the reconciled source of truth.

Before marking DR complete, each stateful component report must include:

- backup configuration source;
- backup object name and terminal phase;
- restore target;
- validation query or artifact digest after restore;
- cleanup performed.

Postgres and ClickHouse should use restore-to-copy drills for routine evidence.
In-place restore is destructive and should be reserved for explicit recovery
drills.

OpenBao is allowed to be unrecoverable from total cluster loss for this phase,
but it still needs a pod/PVC loss drill proving raft replicas and DRBD storage
survive a single-node failure.

## Single-Node Outage Evidence

Kubernetes evacuation rehearsal:

```sh
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-cordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-drain --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-uncordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
```

This proves Kubernetes scheduling and rollout recovery. It is not a substitute
for the required hardware outage drill.

Hardware outage drill:

1. Capture `infra live-snapshot` and `infra talos-health`.
2. Use Latitude OOB/API power control to stop one management node.
3. Capture `infra live-snapshot`, `infra live-rollout`, and `infra talos-health`
   while the node is down.
4. Restore the node.
5. Capture the same commands again after it rejoins.

Passing criteria: the API VIP remains reachable, etcd remains quorate, tenant
site rollouts remain available, replicated storage does not suspend writes for
healthy workloads, and the returned node becomes Ready without manual
Kubernetes object repair.
