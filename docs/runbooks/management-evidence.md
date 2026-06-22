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
aspect infra inventory-check
aspect infra evidence-render
aspect infra plan
aspect infra dns-plan
```

`infra preflight` validates both OpenTofu roots without opening their remote
backends, runs `infra inventory-check`, builds the company-site OCI artifact,
and renders `src/infrastructure/base` with the repo-pinned kubectl.

`infra inventory-check` is a provider-free OpenTofu plan over checked-in files
only. It fails if the inventory's API VIP, node IPs, MetalLB pool, pod MTU, or
Talm values drift away from the manifests that Flux and Talm consume.

`infra evidence-render` renders the opt-in evidence overlay at
`src/infrastructure/evidence`. It is not part of the Flux base; apply it only
when collecting reports.

## Convergence Snapshot

Once the management kubeconfig exists, apply and snapshot the declared platform:

```sh
aspect infra apply-base --kubeconfig "${KUBECONFIG}"
aspect infra seed-db-backup-secret --kubeconfig "${KUBECONFIG}"
aspect infra seed-openbao-evidence-token --kubeconfig "${KUBECONFIG}"
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

`infra seed-db-backup-secret` reads the R2 backup credential contract from
environment variables and applies `Secret/tenant-root/guardian-r2-db-backups`
through the repo-pinned kubectl. It accepts
`GUARDIAN_R2_BACKUP_{BUCKET,ENDPOINT,REGION,ACCESS_KEY_ID,SECRET_ACCESS_KEY}`
or the temporary Cloudflare/AWS variable names documented by
`--help`. Secret values are written to kubectl stdin only.

`infra seed-openbao-evidence-token` reads a non-production OpenBao evidence
token from `GUARDIAN_OPENBAO_EVIDENCE_TOKEN`, `OPENBAO_TOKEN`, `BAO_TOKEN`, or
`VAULT_TOKEN` and applies
`Secret/tenant-root/guardian-openbao-evidence-token` through the repo-pinned
kubectl. Secret values are written to kubectl stdin only.

`infra live-snapshot` expects these resources to exist and be queryable:

- `apps.cozystack.io` app CRs in `tenant-root`: Harbor `oci`, ClickHouse
  `ledger`, Postgres `guardian`, and OpenBAO `guardian`;
- tenants `tenant-root`, `tenant-dev`, and `tenant-gamma`;
- R2 backup Secret contract `tenant-root/guardian-r2-db-backups` by name only;
- OpenBao evidence token Secret contract
  `tenant-root/guardian-openbao-evidence-token` by name only;
- company-site Deployment/Service/Ingress in `tenant-dev`, `tenant-gamma`, and
  `tenant-root`;
- Cozystack backup resources (`BackupClass`, `Plan`, `BackupJob`, `Backup`);
- storage classes and PVC/PV state.

Before the company-site Deployments can pull from Harbor, publish the digest
built by the repo:

```sh
aspect infra publish-company-site
```

This delegates to `//src/products/company/site:push-harbor`, which uses
`rules_oci`'s pinned push toolchain. OCI auth is session secret material; seed
it from secret-zero/OpenBao rather than treating a workstation registry login as
desired state.

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

The opt-in evidence overlay provides:

- `Job/tenant-root/evidence-postgres-load`: 4 concurrent psql workers insert
  and read back 250 rows each through `postgres-guardian-rw`;
- `Job/tenant-root/evidence-clickhouse-load`: 4 concurrent clickhouse-client
  workers insert and read back 250 wide-event rows each through
  `chendpoint-clickhouse-ledger`;
- `Job/tenant-root/evidence-harbor-oci-read`: repeated digest-addressed
  manifest reads from Harbor for the company-site OCI artifact, failing on
  registry errors or digest mismatch;
- `Job/tenant-root/evidence-openbao-load`: health-check OpenBao, ensure the
  `kv/` KV v2 mount exists, then perform 25 token-authenticated write/read
  checks under `kv/guardian/evidence/openbao`;
- `Job/tenant-root/evidence-http-load`: repeated HTTPS requests against
  prod/dev/gamma company-site routes, Harbor health, and the dashboard host;
- `Job/tenant-root/evidence-storage-smoke`: seed/verify a retained replicated
  PVC using deterministic checksums.

Run:

```sh
aspect infra evidence-clean --kubeconfig "${KUBECONFIG}"
aspect infra evidence-apply --kubeconfig "${KUBECONFIG}"
aspect infra evidence-wait --kubeconfig "${KUBECONFIG}" --timeout 30m
aspect infra evidence-restore-apply --kubeconfig "${KUBECONFIG}"
aspect infra evidence-restore-wait --kubeconfig "${KUBECONFIG}" --timeout 30m
aspect infra evidence-logs --kubeconfig "${KUBECONFIG}"
aspect infra evidence-snapshot --kubeconfig "${KUBECONFIG}"
aspect infra evidence-capture \
  --kubeconfig "${KUBECONFIG}" \
  --talosconfig "${TALOSCONFIG}" \
  --phase evidence
```

`evidence-capture` is read-only. It writes command outputs under
`docs/reports/infrastructure/live-runs/<timestamp>-<phase>/` by default,
including `summary.tsv`, Kubernetes snapshots, evidence Job logs, BackupJob and
RestoreJob state, and Talos health when `--talosconfig` is supplied. Commit the
capture directory with the component reports for the live run.

`evidence-clean` deletes completed Jobs, BackupJobs, RestoreJobs, temporary
restore targets, and evidence ConfigMaps. It keeps
`PVC/tenant-root/evidence-replicated-retain` by default so repeat runs verify
the existing checksum manifest. Pass `--delete-pvc=true` only when the report is
explicitly drilling storage data loss.

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
Secret-zero seeding is currently the repo-owned
`aspect infra seed-db-backup-secret` task; once the OpenBao projection
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

The opt-in evidence overlay declares:

- `BackupJob/tenant-root/evidence-postgres-adhoc`;
- restore target `Postgres/tenant-root/guardian-restore-check`;
- `RestoreJob/tenant-root/evidence-postgres-to-copy`;
- `BackupJob/tenant-root/evidence-clickhouse-adhoc`;
- restore target `ClickHouse/tenant-root/ledger-restore-check`;
- `RestoreJob/tenant-root/evidence-clickhouse-to-copy`.

These objects are temporary evidence resources. Do not add
`src/infrastructure/evidence` to the Flux base. Apply the RestoreJobs only after
`aspect infra evidence-wait` has observed both BackupJobs in `Succeeded`.

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
aspect infra evidence-capture --kubeconfig "${KUBECONFIG}" --phase outage-drained
aspect infra outage-uncordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
```

This proves Kubernetes scheduling and rollout recovery. It is not a substitute
for the required hardware outage drill.

Hardware outage drill:

1. Capture `infra live-snapshot` and `infra talos-health`.
   Also run `aspect infra evidence-capture --phase outage-before`.
2. Use Latitude OOB/API power control to stop one management node.
3. Capture `infra live-snapshot`, `infra live-rollout`, and `infra talos-health`
   while the node is down. Also run
   `aspect infra evidence-capture --phase outage-down --allow-failures=true`
   so the report preserves any degraded command output instead of stopping at
   the first failure.
4. Restore the node.
5. Capture the same commands again after it rejoins.
   Also run `aspect infra evidence-capture --phase outage-after`.

Passing criteria: the API VIP remains reachable, etcd remains quorate, tenant
site rollouts remain available, replicated storage does not suspend writes for
healthy workloads, and the returned node becomes Ready without manual
Kubernetes object repair.
