# SpiceDB operations

SpiceDB is the authorization datastore for Guardian product identities and
resources. It runs only in `tenant-guardian-prod`: one namespace-scoped
operator, three SpiceDB replicas, and a three-instance CloudNativePG cluster
created by the Cozystack `Postgres/spicedb` application. The production API is
cluster-internal. Product services receive one of two generated bearer-token
slots through their own workload Secret wiring.

The source of truth is:

- `deployments/authorization/operator` for the CRD, operator, and the
  exact-name PostgreSQL topology policy;
- `deployments/authorization/data` for PostgreSQL, certificates, credentials,
  and backup activation;
- `deployments/authorization/prod` for schema, networking, SpiceDB, load, and
  alerting;
- `load/spicedb-checks.yaml` for the expected allow and deny decisions; and
- `tools/ops/spicedb-qualify` for read-only evidence collection.

No operational procedure in this runbook changes the cluster directly.
Rollouts, restarts, failovers, rotations, backups, restores, upgrades, alert
drills, and cleanup are reviewed source changes. Flux is the only writer.

## Availability and recovery objectives

| Objective | Target |
| --- | --- |
| Authorization availability | Three replicas; two available during planned work |
| PostgreSQL data loss | At most 5 minutes for an idle database; active WAL normally archives sooner |
| PostgreSQL copy-restore | Ready and verified within 30 minutes |
| CheckPermission latency | Establish p50/p95/p99 at 50, 100, 250, and 500 QPS; alert initially at 100 ms p99 |
| Incorrect decisions during planned work | Zero |

PostgreSQL uses synchronous quorum `1..2`, encrypted replicated storage,
`archive_timeout=300s`, continuous R2 WAL archiving, and nightly base backups.
SpiceDB limits each replica to 12 read and 4 write database connections. Three
steady replicas use at most 48 connections; a surge replica during a rollout
keeps the maximum at 64 against PostgreSQL's limit of 100.

## Cold install

The Flux graph installs in this order:

1. the namespace-scoped operator CRD, Role, RoleBinding, Deployment,
   observability, and the admission policy that makes node anti-affinity
   required only for `tenant-guardian-prod/postgres-spicedb`;
2. the PostgreSQL application, backup plan, two generated API credential
   slots, and the certificate chain;
3. the `SpiceDBCluster`; then
4. the versioned schema Job and its positive, negative, TLS, and
   authentication checks.

Start from a cluster converged on `main`, merge the foundation change, and
wait for the exact merge revision:

```sh
tools/ops/cluster-watch --status --until-ready --revision <merge-commit>
tools/ops/spicedb-qualify placement
```

The initial `BackupJob` activates CNPG's archive configuration. Do not treat
its artifact as restorable. Once it succeeds, merge a second, uniquely named
`BackupJob` into `deployments/authorization/data/postgres.yaml` and wait for
that revision before accepting the backup gate. The second base backup is the
first restore source whose WAL range is wholly inside the configured archive
era.

Inspecting objects, status, events, metrics, and logs is read-only and is
permitted. Any correction is a new reviewed commit and another Flux
convergence.

## Functional and security qualification

The schema Job is authoritative for the in-cluster gate. It imports the
checked-in schema and relationships, then proves:

- Alice can manage Guardian;
- Mallory cannot manage Guardian;
- the correct CA and service hostname establish TLS;
- an unrelated CA and a wrong hostname cannot establish TLS; and
- an invalid bearer token is rejected with HTTP 401 or 403.

The same checks can be repeated through the gRPC API. The API token is born in
the cluster and is intentionally unreadable to the normal platform-agent
role. Acquire explicitly audited Secret-read access, then run:

```sh
tools/ops/spicedb-qualify verify
```

The command opens only a local port-forward, uses the repository-pinned `zed`,
and removes its temporary CA material on exit.

## Load baseline and decision-safety gate

For each baseline, change `--qps` in `thumper.yaml`, merge it, and wait for
Flux convergence. Then observe the deployed load for at least ten minutes:

```sh
tools/ops/spicedb-qualify load 10m 50
tools/ops/spicedb-qualify load 10m 100
tools/ops/spicedb-qualify load 10m 250
tools/ops/spicedb-qualify load 10m 500
```

The command never starts an ad-hoc workload or changes the Deployment. It
refuses to measure a QPS value other than the one reconciled from Git and
requires the deployed client to verify both the CA and hostname. Thumper sends
a 90/10 mix of expected allow and expected deny checks with full consistency.
Raw client histogram deltas, status-code counters, and logs land under
`.guardian/evidence/spicedb/`, which is deliberately ignored source state.
Record achieved QPS, p50/p95/p99, error ratio, CPU, memory, PostgreSQL
connections, and saturation in the rollout PR. An unexpected allow or deny is
an unconditional failure. A transport error during a deliberate failover is
reported separately and does not hide an incorrect decision.

The scenario writes and deletes a relationship around its permission checks.
If either mutation returns an error, the server-side outcome is unknown and
the dependent check is classified as indeterminate rather than correct or
incorrect. The qualifier preserves the raw mismatch count, reports these
indeterminate outcomes separately, and fails on every mismatch whose
prerequisite mutation succeeded.

For a disruptive rehearsal, start a longer load command in one terminal and
merge exactly one drill change from another. Keep the traffic running from
before the new revision is observed until the cluster has returned to steady
state. Preserve the Thumper evidence directory with the PR and cluster-watch
links.

## Rolling SpiceDB restart

Change `guardian.dev/qualification-restart` under
`spec.config.extraPodAnnotations` to a new UTC rehearsal identifier. The
operator replaces one replica at a time. Required anti-affinity occupies all
three nodes, so the Deployment uses no surge and permits one unavailable
replica while the PDB preserves a two-ready floor. Each replacement must
remain Ready for 30 seconds before the next replica is removed so endpoint
propagation and client reconnection settle between replacements. Merge the
change, wait for the merge revision, and require:

- three Ready replicas at completion on three distinct nodes;
- no Thumper `wrong permissionship` records;
- at least two Ready replicas throughout the rollout; and
- no sustained alert other than the expected restart warning.

Reusing an old annotation value is not a drill. Never remove a pod to provoke
this path.

## PostgreSQL primary failover

Exercise a planned primary handoff by changing one reload-incompatible
PostgreSQL parameter to a safe, reviewed value. A one-step `shared_buffers`
change is the standard rehearsal. Cozystack and CNPG reconcile the change and
perform the rolling instance update and primary handoff.

Run Thumper across the entire Flux reconciliation. Require a new healthy
primary, three Ready PostgreSQL instances on separate nodes, zero incorrect
decisions, and recovery inside the authorization error budget. Revert the
parameter through a second PR after recording the evidence. Never remove the
primary pod or invoke a CNPG promotion command for this drill.

## API credential rotation

SpiceDB accepts independent token slots A and B. Product callers switch slots
before the old slot changes:

1. confirm every caller is using slot B;
2. change slot A's ExternalSecret `guardian.dev/rotation` audit identity and
   `force-sync` annotation, plus the SpiceDB qualification rollout annotation,
   in the same PR; the Password generator's length and character policy remain
   unchanged;
3. after Flux convergence, prove the new A and unchanged B credentials work;
4. move callers to A through their own reviewed rollout;
5. rotate B in the same way; and
6. repeat the functional gate.

At no point is the generated value committed, copied to OpenBao, or printed in
review evidence. A slot is not retired until metrics show that no caller uses
it.

## Certificate rotation

The leaf key is ECDSA and `rotationPolicy: Always`. To rehearse renewal, add a
unique, valid DNS SAN to `Certificate/spicedb-server` in a reviewed PR.
cert-manager issues a new key and leaf while the CA remains stable. The
declarative secret reloader triggers the same one-at-a-time SpiceDB rollout used
for other planned changes.

Keep Thumper running, wait for the Certificate's observed generation and
revision to advance and all three new pods to become Ready, repeat TLS
verification, and require zero incorrect decisions. Remove the rehearsal SAN
in a follow-up PR; that second issuance proves repeated rotation. A CA
replacement is a separate migration: introduce overlapping old/new trust,
issue the new leaf, migrate every client, and only then remove the old root.
Do not collapse those phases into one change.

## Minor upgrade and rollback

The operator graph maps versions to exact index digests. The first installation
deliberately uses SpiceDB v1.52.0 only while network-isolated from product
callers. The v1.54.0 PostgreSQL migration only adds and populates the unified
schema tables; it does not remove the v1.52.0 tables. Under Thumper load:

1. take and verify the pre-upgrade R2 copy;
2. change `spec.version` to `v1.54.0`, merge, wait for the migration and
   one-at-a-time rollout, and run the complete functional gate;
3. rehearse the explicitly bounded rollback by temporarily setting
   `config.image` to the checked v1.52.0 digest, `config.skipMigrations: true`,
   and `config.datastoreAllowedMigrations: populate-schema-tables`;
4. merge, prove v1.52.0 serves the forward migration with zero incorrect
   decisions, and do not write a new product schema during this interval; then
5. remove those three rollback overrides, leave `spec.version: v1.54.0`, merge,
   and repeat the gates.

Those overrides permit this one additive-database rollback; they are not
standing configuration. A schema or datastore downgrade is never improvised.
If the old binary does not become Ready, recover forward or restore the
pre-upgrade R2 copy into a new application.

## R2 copy-restore drill

The drill is non-destructive and is built as a sequence of small PRs:

1. merge a uniquely named second `BackupJob` after archive activation;
2. after it succeeds, merge a one-shot SQL Job that writes a UUID and UTC
   timestamp marker to the source;
3. wait at least one `archive_timeout`, then merge a one-replica scratch
   `Postgres/spicedb-restore-<date>`, a `RestoreJob` referencing the trusted
   BackupJob, and a verification Job;
4. the verification Job connects with the source application's credential
   because physical recovery preserves the source role's password hash, and
   succeeds only if the post-base-backup marker is present;
5. prove the latest source WAL segment available when the restore began was
   replayed, calculate RPO from that segment's archive time, and calculate RTO
   from RestoreJob creation to successful verification; and
6. remove the scratch application, RestoreJob, and verification Job from
   source in a cleanup PR, then wait for Flux to prune them; and
7. if the application deletion leaves its Helm release storage and CNPG
   resources behind, re-adopt that exact release as a direct, shard-labelled
   Flux `HelmRelease`, wait for it to become Ready with
   `finalizers.fluxcd.io`, then remove it in a second PR so helm-controller
   performs the uninstall. Verify that the CNPG cluster, pods, PVCs, release
   secrets, credentials, and certificates are all absent.

Require RPO at or below five minutes and RTO below thirty minutes. Never use an
in-place RestoreJob in a rehearsal. The source remains serving throughout.

## Alert delivery

Normal rules cover replica loss, request errors, p99 latency, restarts,
migration failures, and schema failures. To test end-to-end delivery, change
`SpiceDBAlertPathDrill` from `vector(0)` to `vector(1)` in a reviewed PR. Wait
for the notification to arrive through the production Alertmanager/Alerta
path, record its timestamps, then restore `vector(0)` in a second PR and prove
the alert resolves.

An alert test is incomplete until both firing and resolution notifications
arrive. Leave no drill rule enabled.

## Exit evidence

The workstream closes only with links or captured output for every row:

| Gate | Evidence |
| --- | --- |
| Flux cold install | exact revision reaches Ready from absent resources |
| Namespace RBAC | `spicedb-qualify placement` and no operator ClusterRoleBinding |
| Fault domains | three distinct SpiceDB nodes and three distinct PostgreSQL nodes |
| TLS and tokens | schema Job plus `spicedb-qualify verify` |
| Schema | local `zed validate`, schema Job, and positive/negative live checks |
| SpiceDB restart | Thumper summary spanning the Flux rollout |
| PostgreSQL failover | Thumper summary, new primary, and recovery interval |
| Rotations | Secret/certificate revisions and repeated live checks |
| Upgrade/rollback | three converged revisions ending at the target release |
| R2 restore | marker, BackupJob/RestoreJob status, measured RPO/RTO, cleanup revision |
| Capacity | numeric QPS and latency/resource table |
| Alerts | firing and resolved delivery timestamps |

If any row lacks evidence, the prerequisite remains open.
