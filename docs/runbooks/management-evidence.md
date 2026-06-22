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
checks that the built company-site digest matches the environment manifests,
base Deployments, and Harbor evidence fixture, and renders
`src/infrastructure/base` with the repo-pinned kubectl.

`infra inventory-check` is a provider-free OpenTofu plan over checked-in files
only. It fails if the inventory's API VIP, node IPs, MetalLB pool, pod MTU, or
Talm values drift away from the manifests that Flux and Talm consume.

`infra evidence-render` renders the opt-in evidence overlay at
`src/infrastructure/evidence`. It is not part of the Flux base; apply it only
when collecting reports.

## Convergence Snapshot

Once the management kubeconfig exists, apply and snapshot the declared platform:

```sh
aspect infra management-converge-run \
  --kubeconfig "${KUBECONFIG}" \
  --talosconfig "${TALOSCONFIG}"
```

`management-converge-run` is the preferred live convergence command before
collecting evidence. It runs provider-free preflight checks, applies
`src/infrastructure/base`, seeds the R2 backup and OpenBao evidence Secret
contracts from environment variables, reapplies the base after the Secret
contracts exist, pushes the company-site OCI image to Harbor, checks
dev/gamma/prod rollouts, checks Talos/etcd health, applies the Cloudflare DNS
root, and prints a live snapshot. Use `--skip-secret-seed`, `--skip-publish`,
`--skip-dns`, or `--skip-preflight` only for a deliberate rerun where that
prerequisite has already been handled.

The equivalent expanded sequence is:

```sh
aspect infra apply-base --kubeconfig "${KUBECONFIG}"
aspect infra seed-db-backup-secret --kubeconfig "${KUBECONFIG}"
aspect infra seed-openbao-evidence-token --kubeconfig "${KUBECONFIG}"
aspect infra apply-base --kubeconfig "${KUBECONFIG}"
aspect infra publish-company-site
aspect infra live-rollout --kubeconfig "${KUBECONFIG}"
aspect infra talos-health --talosconfig "${TALOSCONFIG}"
aspect infra dns-apply
aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"
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

Before the company-site Deployments can pull from Harbor, the digest built by
the repo must be published. `management-converge-run` does this by default; the
standalone command is:

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
  prod/dev/gamma company-site routes, Harbor health, and the dashboard host,
  emitting one `http-target` summary per URL;
- `Job/tenant-root/evidence-storage-smoke`: seed/verify a retained replicated
  PVC using deterministic checksums.

Run the full live evidence package:

```sh
LATITUDESH_AUTH_TOKEN="${LATITUDESH_AUTH_TOKEN}" \
aspect infra management-evidence-run \
  --kubeconfig "${KUBECONFIG}" \
  --talosconfig "${TALOSCONFIG}"
```

`management-evidence-run` is the preferred final live command. It applies the
opt-in evidence overlay after checking base app, secret-contract, and tenant
surface prerequisites, waits for the load jobs and database backup/restore
drills, captures and verifies the load/DR evidence, power-cycles each selected
Latitude management node sequentially through `hardware-outage-run-all`, and
writes a suite verification report. The default parent output directory is
`docs/reports/infrastructure/live-runs/<timestamp>-management-evidence/` with
these children:

- `evidence/`: load, storage, app, backup, restore, Kubernetes, and Talos
  capture plus `VERIFY.md`;
- `hardware-outage-all/`: one verified hardware outage run per management node;
- `management-suite/`: `SUITE.md` and `suite-verification.tsv` tying the load,
  DR, and outage evidence together.

The command requires a Talos config because final suite verification requires
Talos health in the load/DR capture and in the before/after outage phases. It
also requires a Latitude API token because it performs true hardware power
actions.

For targeted debugging, run the load/DR portion alone:

```sh
aspect infra evidence-run \
  --kubeconfig "${KUBECONFIG}" \
  --talosconfig "${TALOSCONFIG}" \
  --phase evidence \
  --timeout 30m
```

The equivalent expanded sequence is:

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
aspect infra evidence-verify \
  --run-dir docs/reports/infrastructure/live-runs/<timestamp>-evidence \
  --mode evidence \
  --require-talos
```

`evidence-restore-apply` creates the database RestoreJobs and the restored-copy
query verification Jobs. `evidence-restore-wait` requires both the RestoreJobs
and those verification Jobs to complete before the run can pass.

`evidence-capture` is read-only. It writes command outputs under
`docs/reports/infrastructure/live-runs/<timestamp>-<phase>/` by default,
including `summary.tsv`, Kubernetes snapshots, an API VIP `/readyz` read-load
summary, evidence Job logs, BackupJob and RestoreJob state, and Talos health
when `--talosconfig` is supplied. Commit the capture directory with the
component reports for the live run.

`evidence-verify` reads a captured live-run directory and writes `VERIFY.md`
plus `verification.tsv` next to the raw outputs. For `--mode evidence`, it
checks the command-status summary, API VIP load summary, required app CRs,
tenant company-site resources and ready replicas, ingress hosts, evidence Job
completions, stable load-test log summaries, per-target HTTP summaries, and
BackupJob/RestoreJob success markers plus restored-copy query logs. Treat this
as report input, not as a substitute for reviewing the raw evidence.

`evidence-run` runs the expanded load/DR sequence in
order and attempts `evidence-capture` even when an earlier wait/log/snapshot
step fails, preserving the degraded state for the report.

After standalone `evidence-verify` passes and the all-node hardware outage
capture exists, write the suite-level report:

```sh
aspect infra evidence-verify-suite \
  --evidence-dir docs/reports/infrastructure/live-runs/<timestamp>-evidence \
  --hardware-outage-dir docs/reports/infrastructure/live-runs/<timestamp>-hardware-outage-all \
  --out-dir docs/reports/infrastructure/live-runs/<timestamp>-management-suite
```

`evidence-verify-suite` is read-only against captured evidence. It checks that
the load/DR capture has a passing `VERIFY.md` with Talos required, that the
required component load and restore checks are present, that every management
node from `src/infrastructure/inventory/guardian-mgmt.json` has Latitude
power/status records, and that each node has passing `outage-before`,
`outage-down`, and `outage-after` verification reports. The before/after
outage phases must also have Talos required. Commit the suite directory with
the raw live-run directories and component reports.

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
- `Job/tenant-root/evidence-postgres-restore-verify`;
- `BackupJob/tenant-root/evidence-clickhouse-adhoc`;
- restore target `ClickHouse/tenant-root/ledger-restore-check`;
- `RestoreJob/tenant-root/evidence-clickhouse-to-copy`;
- `Job/tenant-root/evidence-clickhouse-restore-verify`.

These objects are temporary evidence resources. Do not add
`src/infrastructure/evidence` to the Flux base. Apply the RestoreJobs only after
`aspect infra evidence-wait` has observed both BackupJobs in `Succeeded`.

OpenBao is allowed to be unrecoverable from total cluster loss for this phase,
but it still needs a pod/PVC loss drill proving raft replicas and DRBD storage
survive a single-node failure.

## Single-Node Outage Evidence

Kubernetes evacuation rehearsal:

```sh
aspect infra outage-run \
  --kubeconfig "${KUBECONFIG}" \
  --talosconfig "${TALOSCONFIG}" \
  --node <node> \
  --timeout 10m
```

The equivalent expanded sequence is:

```sh
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-cordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-drain --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra evidence-capture --kubeconfig "${KUBECONFIG}" --phase outage-drained
aspect infra evidence-verify \
  --run-dir docs/reports/infrastructure/live-runs/<timestamp>-outage-drained \
  --mode outage \
  --node <node>
aspect infra outage-uncordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
```

`outage-run` is the preferred Kubernetes-side rehearsal command. It captures
`outage-before`, `outage-drained`, and `outage-after` evidence directories and
attempts an `outage-failed` capture if a step fails. This proves Kubernetes
scheduling and rollout recovery. It is not a substitute for the required
hardware outage drill. Run `aspect infra evidence-verify --mode outage` against
each captured outage directory before attaching it to the outage report; pass
`--min-ready-nodes 2` for a true `outage-down` hardware capture.

Standalone hardware outage drill:

```sh
LATITUDESH_AUTH_TOKEN="${LATITUDESH_AUTH_TOKEN}" \
aspect infra hardware-outage-run-all \
  --kubeconfig "${KUBECONFIG}" \
  --talosconfig "${TALOSCONFIG}" \
  --require-talos
```

`hardware-outage-run-all` is the standalone final outage report command. It reads
the management node list from
`src/infrastructure/inventory/guardian-mgmt.json` and runs the true single-node
outage drill once per node, sequentially.

Use the per-node command for a targeted rerun:

```sh
LATITUDESH_AUTH_TOKEN="${LATITUDESH_AUTH_TOKEN}" \
aspect infra hardware-outage-run \
  --kubeconfig "${KUBECONFIG}" \
  --talosconfig "${TALOSCONFIG}" \
  --node <node> \
  --require-talos
```

`hardware-outage-run` is the per-node true single-node outage command. It:

- records Latitude status before the outage;
- captures and verifies `outage-before`;
- sends Latitude `power_off` and waits for server status `off`;
- captures and verifies `outage-down` with the target node `NotReady` and two
  Ready Kubernetes nodes required;
- sends Latitude `power_on` and waits for server status `on`;
- captures and verifies `outage-after` with the target node Ready again.

If a capture or verification step fails after `power_off`, the runner attempts
Latitude `power_on` during exit cleanup before returning failure. Treat the
failed run directory as incident evidence, then confirm the server status before
starting another drill.

The default output directory is
`docs/reports/infrastructure/live-runs/<timestamp>-hardware-outage-all/` for the
all-node command, and
`docs/reports/infrastructure/live-runs/<timestamp>-hardware-outage-<node>/` for
the per-node command. Each per-node directory contains `latitude-before.jsonl`,
`latitude-down.jsonl`, `latitude-after.jsonl`, and one capture directory per
phase. Run `aspect infra evidence-verify-suite` after the all-node command
finishes, then commit the parent directory with the final outage report.

Equivalent manual sequence:

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

Latitude API contract for the power step:

```http
POST https://api.latitude.sh/servers/{server_id}/actions
Authorization: Bearer ${LATITUDESH_AUTH_TOKEN}
Content-Type: application/vnd.api+json

{
  "data": {
    "type": "actions",
    "attributes": {
      "action": "power_off"
    }
  }
}
```

Use `power_on` to restore the node. Poll
`GET https://api.latitude.sh/servers/{server_id}` and record
`data.attributes.status` before the down capture and after the restored capture.
The management server IDs are checked into
`src/infrastructure/inventory/guardian-mgmt.json`.

The Aspect task uses the repo-built `//src/tools/latitude:latitude-power`
binary, not a workstation `curl` or provider CLI.
