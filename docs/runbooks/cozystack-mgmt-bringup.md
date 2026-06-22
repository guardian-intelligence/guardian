# Cozystack Management Cluster Bring-Up

This runbook describes the current `guardian-mgmt` substrate checked into this
repo: a three-node Talos/Cozystack management cluster on one Latitude.sh Virtual
Network with real L2/ARP. It replaces the old public-/31 KubeSpan procedure.

The source files are the authority. This document explains their shape and the
checks to run; it is not a separate source of truth.

## Source Of Truth

`src/infrastructure/bootstrap/guardian-mgmt/main.tf` is the non-secret topology
record for Latitude project/site, the management VLAN, and the three adopted
control-plane servers. The manifest invariant test checks that OpenTofu imports,
Talm values, Cozystack platform publishing, MetalLB, and Kube-OVN stay aligned
with that HCL, and that `outputs.tf` exposes the management VLAN and
control-plane node map as standard OpenTofu outputs. Change the HCL and
dependent manifests together; do not let topology drift through one-off edits.

| Layer | File |
| - | - |
| Bare-metal and VLAN state | `src/infrastructure/bootstrap/guardian-mgmt/*.tf` |
| OpenBao API configuration | `src/infrastructure/bootstrap/guardian-mgmt-openbao/*.tf` |
| Talos/Talm chart | `src/infrastructure/talm/` |
| Cozystack platform package | `src/infrastructure/base/cozystack/platform.yaml` |
| Core Cozystack apps | `src/infrastructure/base/apps/core-services.yaml` |
| Backup strategy classes | `src/infrastructure/base/backup/` |
| OpenBao-backed secret delivery | `src/infrastructure/base/secrets/`, `src/infrastructure/environments/*/secrets.yaml` |
| MetalLB L2 pool | `src/infrastructure/base/networking/metallb.yaml` |
| Kube-OVN MTU | `src/infrastructure/base/networking/subnet-mtu.yaml` |
| Flux handoff | `src/infrastructure/base/flux/sync.yaml` |
| OpenBao app | `src/infrastructure/base/openbao/` |
| LINSTOR storage classes | `src/infrastructure/base/storage/storageclasses.yaml` |
| Environment tenants | `src/infrastructure/base/tenants/environments.yaml` |
| Environment Cozystack apps | `src/infrastructure/environments/` |
| Company-site OCI artifact | `src/products/company/site/` |

## Current Facts

`guardian-mgmt` uses the Latitude project `proj_R82A0yqmd06mM` in ASH and
Virtual Network `vlan_8mop5gkpP5jxv`, VID `2140`.

| Node | Latitude ID | Public IPv4 | VLAN IPv4 |
| - | - | - | - |
| `ash-earth` | `sv_vAPXaMxKM5epz` | `206.223.228.101` | `10.8.0.11` |
| `ash-wind` | `sv_nPRbajqEB5koM` | `45.250.254.119` | `10.8.0.12` |
| `ash-water` | `sv_8mop5gZo8Njxv` | `206.223.228.87` | `10.8.0.13` |

The Kubernetes API endpoint is the Talos Layer2 VIP
`https://10.8.0.250:6443`, pinned to `enp1s0f0.2140`.

The checked-in OpenTofu root also exposes this topology through standard
outputs. Use those outputs as the machine-readable interface for scripts,
Aspect tasks, and future bootstrap CLI code; do not add a parallel inventory
file for the same node/IP/VLAN facts.

```sh
aspect infra topology --name management_vlan
aspect infra topology --name control_plane_nodes
```

The task is a thin wrapper over the repo-pinned OpenTofu artifact and prints
`tofu output -json`; it does not define a Guardian-specific schema.

Expected network shape:

- etcd, kubelet node IP selection, and kube-ovn node traffic use `10.8.0.0/24`.
- KubeSpan is not part of this topology; there should be no KubeSpan mesh links.
- MetalLB advertises private LoadBalancer services from `10.8.0.200-10.8.0.240`.
- Public ingress still uses the three public node IPs in the Cozystack platform
  package.
- Kube-OVN subnet MTU is `1362`: `1420` VLAN path MTU minus `58` bytes of GENEVE.
- The default StorageClass is `replicated`: three-way LINSTOR/DRBD on the
  checked-in `data` pool. `local` and `local-retain` remain available only for
  explicitly selected scratch or intentionally node-local state.

## Validate Checked-In Substrate

The repo pins OpenTofu in `MODULE.bazel`, the Latitude provider in
`src/infrastructure/bootstrap/guardian-mgmt/.terraform.lock.hcl`, and the Vault
provider in
`src/infrastructure/bootstrap/guardian-mgmt-openbao/.terraform.lock.hcl`.

Run the full local substrate check with:

```sh
aspect infra validate
```

`aspect infra validate` runs OpenTofu `fmt`/`init -backend=false`/`validate` for
both bootstrap roots, builds the infrastructure filegroups, runs the active
manifest invariant test, and renders both Kustomize roots with the repo-pinned
kubectl artifact.

Run the active invariant test directly with:

```sh
bazelisk test //src/infrastructure/tests:manifest_invariants_test
```

That test parses the checked-in Kubernetes YAML and uses the repo-pinned `talm`
binary to render the management control-plane template offline with throwaway
secrets under the Bazel test temp directory. It verifies the platform package
publishes the dashboard/API endpoints, environment tenants use the expected
`*.gi.org` hosts, OpenTofu/Talm/Kubernetes manifests stay aligned with the
management OpenTofu topology, the rendered Talos config keeps the L2 VIP and
VLAN control-plane path, KubeSpan/WireGuard stays absent, MetalLB and Kube-OVN
keep the L2/MTU topology, `replicated` is the only default StorageClass, root
and environment Postgres/Harbor/ClickHouse apps use the intended HA/storage
shape, OpenBao stays declared in `tenant-root`, the OpenBao OpenTofu root
declares only mounts, policies, and Kubernetes-auth roles, the reusable CNPG
backup strategy maps through a cluster-scoped BackupClass, OpenBao-backed CNPG
backup credential projections exist for root/dev/gamma/prod, the company site
is declared for dev/gamma/prod, and Flux reconciles base before tenant apps.

After a PR is merged to `main`, validate that the live management cluster's
source-controller has reconciled the merged commit with:

```sh
aspect infra live \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "<merged-main-commit-sha>"
```

`aspect infra live` uses the repo-pinned kubectl artifact, refuses to validate
against the excluded Verself-prod API at `206.223.228.99`, refuses kubeconfigs
whose cluster server is outside the management OpenTofu API endpoint set,
requires exactly three management nodes with `10.8.0.x` InternalIP addresses,
waits for the Flux source and both Guardian Kustomizations to become Ready,
verifies their applied revision contains the expected merged commit, and checks
the declared Cozystack dashboard, app, networking, storage, OpenBao, backup,
and company-site resources exist. For Cozystack apps, the live gate also waits
on the aggregated app resources' standard conditions: `Ready` for tenant and
service Helm reconciliation, and `WorkloadsReady` for monitored
Postgres/Harbor/ClickHouse workloads. For the dev/gamma/prod company-site
surfaces, it also checks that live pods are scheduled on three distinct
Kubernetes nodes, matching the strict hostname topology spread declared in the
manifests.

If `aspect infra live` fails before node discovery with an x509 verification
error, treat the local kubeconfig/Talos operator state as stale. Refresh the
operator credentials from the current cluster state before rerunning the live
gate:

```sh
aspect infra kubeconfig
export GUARDIAN_MGMT_KUBECONFIG="$PWD/src/infrastructure/talm/kubeconfig"
```

`aspect infra kubeconfig` runs repo-pinned `talm talosconfig` first so expired
Talos client certificates are regenerated from the gitignored local Talm
secrets, then runs repo-pinned `talm kubeconfig --merge=false --force` against
the VLAN VIP and writes `src/infrastructure/talm/kubeconfig`. If the Talos CA
itself was rotated and the local Talm secrets are stale, restore or regenerate
the operator state through the bootstrap path instead. Do not use insecure TLS
flags for source-controller validation.

Local validation does not require backend credentials:

```sh
bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" fmt -check

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" \
  init -backend=false -input=false -reconfigure

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" validate

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt-openbao" fmt -check

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt-openbao" \
  init -backend=false -input=false -reconfigure

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt-openbao" validate
```

Live planning requires:

- `LATITUDESH_AUTH_TOKEN` for the Latitude provider.
- S3-compatible backend credentials for R2 through the usual `AWS_*`
  environment variables.
- The R2 endpoint passed during backend initialization, because OpenTofu's S3
  backend cannot read it from HCL locals.
- `VAULT_TOKEN` when planning or applying
  `src/infrastructure/bootstrap/guardian-mgmt-openbao`; pass
  `-var=openbao_addr=...` when planning through a local port-forward instead of
  the default in-cluster service address.

```sh
bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" \
  init -input=false -reconfigure \
  -backend-config="endpoint=$AWS_ENDPOINT_URL_S3"

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" plan -input=false
```

The checked-in import blocks adopt the known Virtual Network and three servers.
`latitudesh_vlan_assignment.control_plane` is declared, but the provider imports
VLAN assignments by provider-side assignment ID. Add those imports only after
discovering the assignment IDs from Latitude.

## Talos L2 Render Inputs

The checked-in Talm chart sets:

- `endpoint: https://10.8.0.250:6443`
- `floatingIP: 10.8.0.250`
- `vipLink: enp1s0f0.2140`
- `advertisedSubnets: ["10.8.0.0/24"]`
- cert SANs for the VIP and each public node IP
- Talos image `ghcr.io/cozystack/cozystack/talos:v1.12.6`
- Kubernetes `v1.34.3`

Generated Talm secrets, rendered node configs, kubeconfig, and local operator
state must stay out of Git. `src/infrastructure/talm/.gitignore` excludes those
paths.

The invariant test renders the checked-in control-plane template with the
repo-pinned `talm` artifact and scratch secrets only. The next bootstrap CLI
slice should reuse that chart and the repo-pinned `talm`/`talosctl` artifacts to
render each node, apply the first config in Talos maintenance mode, bootstrap
etcd exactly once, and persist the encrypted genesis bundle under operator
state.

## Kubernetes Handoff

After Talos is bootstrapped and Cozystack is installed, apply the Flux handoff
once:

```sh
kubectl apply -f src/infrastructure/base/flux/sync.yaml
```

Flux first reconciles `src/infrastructure/base`, including the Platform package,
root Postgres/Harbor/ClickHouse apps, the CNPG backup strategy and BackupClass,
root CNPG backup credential projection, networking manifests, storage classes,
environment tenants, OpenBao, and the Flux objects themselves. The base also
declares a second Flux Kustomization,
`guardian-mgmt-tenant-apps`, that depends on `guardian-mgmt-base` and reconciles
`src/infrastructure/environments` after the Tenant chart has had a chance to
create `tenant-dev`, `tenant-gamma`, and `tenant-prod`. The environment layer
also declares each tenant's CNPG backup credential projection.

Both Flux Kustomizations are apply-only (`wait: false`). Cozystack app CRs fan
out into HelmReleases and stateful workloads; service readiness is proven by
the Cozystack app resources' `Ready` and `WorkloadsReady` conditions plus the
component-specific live drill output, not by treating Flux's apply status as
service health.

After changing checked-in infrastructure, merge the PR to `main` and let the
existing `GitRepository/guardian` and `Kustomization/guardian-mgmt-*` objects
pull the new revision. Do not manually apply the rendered base as a substitute
for source-controller validation; manual apply is only the bootstrap handoff
before Flux owns the path.

The platform package uses Cozystack's `isp-full` variant. In Cozystack 1.4,
`isp-full` includes the backup controller and backupstrategy controller, but
`cozystack.external-secrets-operator` and `cozystack.velero` are optional
system packages. Guardian enables both through
`bundles.enabledPackages` so later OpenBao-backed Secret projection and
Velero-backed backup/restore evidence can reconcile without an out-of-band
package install. The live gate checks the Cozystack backup packages,
controller deployments, RBAC bindings, and backup CRDs directly before it
checks Guardian's reusable BackupClass resources.

For a direct render check from the repo-pinned kubectl artifact:

```sh
bazelisk build @kubectl_linux_amd64//file:file
OUTPUT_BASE="$(bazelisk info output_base)"
"$OUTPUT_BASE/external/+http_file+kubectl_linux_amd64/file/kubectl" \
  kustomize src/infrastructure/base
```

For the live source-controller gate:

```sh
aspect infra live \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "<merged-main-commit-sha>"
```

Expected success ends with:

```text
guardian-mgmt source-controller has reconciled <commit-sha>
```

For the Talos-side L2 gate:

```sh
aspect infra talos
```

Expected success ends with:

```text
guardian-mgmt Talos L2 checks passed
```

## Cozystack App Path

Cozystack 1.4 serves `apps.cozystack.io/v1alpha1` resources through its
aggregated API server. The API server reads `ApplicationDefinition` objects at
startup, then converts app resources such as `Tenant`, `Postgres`, `Harbor`, and
`ClickHouse` into Flux `HelmRelease` objects. The aggregated app resource
mirrors HelmRelease `Ready` into `.status.conditions` and adds
`WorkloadsReady` when the chart declares Cozystack `WorkloadMonitor` resources.

For `Tenant`, the Cozystack source sets `release.prefix: tenant-`. Applying
`Tenant/dev` in `tenant-root` therefore creates a `HelmRelease` named
`tenant-dev` in `tenant-root`. The tenant chart then creates namespace
`tenant-dev` and writes that namespace's `cozystack-values` Secret. The checked
in `dev`, `gamma`, and `prod` tenants intentionally inherit root `etcd`,
ingress, monitoring, and SeaweedFS; they only set explicit environment hostnames
for now.

The same source-level naming rule applies to Guardian's core services:
`Postgres/guardian` becomes `HelmRelease/postgres-guardian`,
`Harbor/guardian` becomes `HelmRelease/harbor-guardian`,
`ClickHouse/guardian` becomes `HelmRelease/clickhouse-guardian`, and
`OpenBAO/guardian` becomes `HelmRelease/openbao-guardian`. The OpenBao app then
renders its own nested system HelmRelease, `openbao-guardian-system`.
The Harbor app follows the same pattern and renders its nested system
HelmRelease as `harbor-guardian-system`. It also renders standard COSI
`BucketClaim/harbor-guardian-registry` and
`BucketAccess/harbor-guardian-registry` objects, with bucket credentials written
to `Secret/harbor-guardian-registry-bucket`; the nested system chart then
translates the COSI `BucketInfo` into
`Secret/harbor-guardian-registry-s3` for Harbor's S3 registry storage. The live
gate checks those resources directly instead of relying only on the top-level
`Harbor/guardian` condition.

The Postgres app renders `Cluster/postgres-guardian` through CloudNativePG and
`WorkloadMonitor/postgres-guardian`. The ClickHouse app renders
`ClickHouseInstallation/clickhouse-guardian`,
`ClickHouseKeeperInstallation/clickhouse-guardian-keeper`,
`VMPodScrape/clickhouse-guardian-keeper`,
`Secret/clickhouse-guardian-backup-api-auth`, and ClickHouse/keeper
WorkloadMonitors through the Altinity and VictoriaMetrics APIs. `aspect infra
live` checks those child resources and selected spec fields so the app CRs,
operator-facing CRs, and backup sidecar wiring stay aligned.

Important source finding for the next app slice: Cozystack 1.4 `Postgres` and
`Harbor` templates honor `spec.storageClass`, but the `ClickHouse` chart exposes
`spec.storageClass` without rendering it into ClickHouse or keeper PVC
templates. Because Guardian makes `replicated` the cluster default
StorageClass, ClickHouse PVCs that omit `storageClassName` still land on the
three-way DRBD class. Keep specifying `spec.storageClass: replicated` for
operator intent, but treat actual placement as default-class driven until the
upstream chart renders the field.

The checked-in root app slice declares:

- `Postgres/guardian` in `tenant-root`: CNPG-backed, three replicas, explicit
  `storageClass: replicated`, synchronous commit quorum `1..2`.
- `Harbor/guardian` in `tenant-root`: `harbor.guardianintelligence.org`, with
  replicated storage for the registry database, Redis, Trivy, and chart-owned
  PVCs.
- `ClickHouse/guardian` in `tenant-root`: three ClickHouse replicas plus
  three Keeper replicas. `spec.storageClass: replicated` records operator
  intent, while the replicated default StorageClass is what places PVCs on
  DRBD until the upstream chart renders that field. Backup integration is
  enabled through the pre-existing `guardian-clickhouse-backup-creds` Secret,
  and a native Cozystack `Plan/guardian-clickhouse-daily` schedules the
  `BackupClass` flow at `17 1 * * *`.

ClickHouse backups are enabled only by pointing app specs at the Kubernetes
Secrets delivered from the declared OpenBao/R2 secret path; raw S3 credentials
must never be placed directly in app specs. Postgres backups remain disabled
until the non-secret object-store endpoint URL and destination prefixes are
declared for each app.

The base backup layer declares the reusable strategy/classes that do not depend
on tenant secret material:

- `CNPG/guardian-postgres-r2` plus `BackupClass/guardian-postgres-cnpg`.
  The strategy reads `destinationPath` and `endpointURL` from each Postgres
  app's own `spec.backup` block and references a tenant-local Secret named
  `<app>-cnpg-backup-creds`.
- `Altinity/guardian-clickhouse-altinity` plus
  `BackupClass/guardian-clickhouse-altinity`. This follows Cozystack 1.4's
  recommended ClickHouse `BackupClass` path but replaces the upstream example's
  runtime `apk add curl jq` pattern with a digest-pinned Python image and a
  standard-library HTTP client.

The checked-in External Secrets layer declares the backup Secrets for
`Postgres/guardian` and `ClickHouse/guardian` in `tenant-root`, `tenant-dev`,
`tenant-gamma`, and `tenant-prod`:

| Namespace | Component | OpenBao role | OpenBao kv-v2 path |
| - | - | - | - |
| `tenant-root` | Postgres | `tenant-root-cnpg-backup` | `guardian/guardian-mgmt/tenant-root/postgres/guardian/cnpg-backup` |
| `tenant-dev` | Postgres | `tenant-dev-cnpg-backup` | `guardian/guardian-mgmt/tenant-dev/postgres/guardian/cnpg-backup` |
| `tenant-gamma` | Postgres | `tenant-gamma-cnpg-backup` | `guardian/guardian-mgmt/tenant-gamma/postgres/guardian/cnpg-backup` |
| `tenant-prod` | Postgres | `tenant-prod-cnpg-backup` | `guardian/guardian-mgmt/tenant-prod/postgres/guardian/cnpg-backup` |
| `tenant-root` | ClickHouse | `tenant-root-clickhouse-backup` | `guardian/guardian-mgmt/tenant-root/clickhouse/guardian/backup` |
| `tenant-dev` | ClickHouse | `tenant-dev-clickhouse-backup` | `guardian/guardian-mgmt/tenant-dev/clickhouse/guardian/backup` |
| `tenant-gamma` | ClickHouse | `tenant-gamma-clickhouse-backup` | `guardian/guardian-mgmt/tenant-gamma/clickhouse/guardian/backup` |
| `tenant-prod` | ClickHouse | `tenant-prod-clickhouse-backup` | `guardian/guardian-mgmt/tenant-prod/clickhouse/guardian/backup` |

Each Postgres path must contain `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`; ESO writes them to `guardian-cnpg-backup-creds` in the
tenant namespace. Each ClickHouse path must contain `bucketName`, `endpoint`,
`region`, `accessKey`, and `secretKey`; ESO writes them to
`guardian-clickhouse-backup-creds` in the tenant namespace. The SecretStores
talk to Cozystack's OpenBao service at
`http://openbao-guardian.tenant-root.svc:8200`, use the `kv` engine with
`version: v2`, and authenticate through the `kubernetes` auth mount with
audience `openbao`.
`tenant-root` also declares a Cilium allow policy for only the Cozystack ESO
controller in `cozy-external-secrets-operator` to reach OpenBao on port 8200.
The matching OpenBao API state is declared with standard OpenTofu resources in
`src/infrastructure/bootstrap/guardian-mgmt-openbao`: `vault_mount` for `kv`,
`vault_auth_backend` plus `vault_kubernetes_auth_backend_config` for
Kubernetes auth, `vault_policy` for each least-privilege read path, and
`vault_kubernetes_auth_backend_role` for each ESO service account. That root
does not write `vault_kv_secret_v2` or `vault_generic_secret` resources, because
R2 credentials would otherwise land in OpenTofu state. There are no checked-in
`BackupJob` resources yet. ClickHouse app backup is enabled and daily
`Plan/guardian-clickhouse-daily` resources are declared in root/dev/gamma/prod;
they require OpenBao to be initialized/unsealed, the OpenTofu root to be
applied, and real kv secret values to exist before the sidecars and scheduled
jobs can succeed.

The checked-in environment app layer declares the same core service set in each
environment namespace:

- `tenant-dev`: `Postgres/guardian`, `Harbor/guardian` at `harbor.dev.gi.org`,
  and `ClickHouse/guardian` with a daily backup Plan at `23 1 * * *`.
- `tenant-gamma`: `Postgres/guardian`, `Harbor/guardian` at
  `harbor.gamma.gi.org`, and `ClickHouse/guardian` with a daily backup Plan at
  `29 1 * * *`.
- `tenant-prod`: `Postgres/guardian`, `Harbor/guardian` at
  `harbor.prod.gi.org`, and `ClickHouse/guardian` with a daily backup Plan at
  `41 1 * * *`.

All environment app specs select `storageClass: replicated` and run three
replicas for the stateful control-plane services so single-node outage drills
exercise the intended topology.

## HTTP Load Drills

Use k6 for HTTP-facing surfaces. The repo pins the standalone Linux amd64 k6
binary in Bazel and runs it through `aspect infra load-http`; do not install k6
on an operator laptop or traffic-serving host.

`aspect infra load-http` runs the same guardian-mgmt kubeconfig guard as
`aspect infra live` by default. If `--revision` is provided, it also verifies
that the live Flux source and Kustomizations have applied that merged commit
before starting the load test. The task then runs the repo k6 script at
`src/infrastructure/load/http-smoke.js` and prints k6's standard CLI summary.
This is the report input; do not wrap it in a Guardian-specific evidence
format.

Company-site load:

```sh
aspect infra load-http \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --surface company-site \
  --stage gamma \
  --vus 10 \
  --duration 2m
```

Harbor registry API load:

```sh
aspect infra load-http \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --surface harbor \
  --stage root \
  --vus 5 \
  --duration 2m
```

Harbor registry data-path load uses ORAS rather than an HTTP-only probe. The
repo pins the standalone Linux amd64 ORAS binary in Bazel and runs it through
`aspect infra load-harbor-registry`. The task fetches the Cozystack-generated
Harbor admin password from `Secret/harbor-guardian-credentials`, logs in using
a temporary ORAS registry config, pushes a temporary payload artifact, fetches
its manifest, pulls the artifact back, and verifies the payload bytes. The
default repository is `library/guardian-smoke`; if the cluster uses a dedicated
project later, pass it with `--repository`.

```sh
aspect infra load-harbor-registry \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --stage gamma \
  --repository library/guardian-smoke \
  --iterations 3 \
  --payload-bytes 65536
```

The report input is the native ORAS command output plus the helper's pushed and
pulled payload digests. Do not wrap it in a Guardian-specific evidence format.

Dashboard load:

```sh
aspect infra load-http \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --surface dashboard \
  --stage root \
  --vus 5 \
  --duration 2m
```

OpenBao health load uses a temporary `kubectl port-forward` to the in-cluster
`Service/openbao-guardian` and targets `/v1/sys/health`:

```sh
aspect infra load-http \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --surface openbao \
  --stage root \
  --vus 5 \
  --duration 2m
```

Surface defaults:

- `company-site`: `https://dev.gi.org/healthz`,
  `https://gamma.gi.org/healthz`, or
  `https://guardianintelligence.org/healthz`, expected status `200`.
- `harbor`: `https://harbor.guardianintelligence.org/v2/` or the
  environment Harbor host, expected status `200` or unauthenticated `401`.
- `dashboard`: `https://dashboard.guardianintelligence.org/`, expected status
  `200` or auth redirect `302`.
- `openbao`: local port-forward to
  `http://127.0.0.1:<port>/v1/sys/health`, accepting OpenBao's documented
  health statuses for active, standby, sealed, uninitialized, and standby perf
  modes.

For a one-off local k6 smoke check that is not evidence for the management
cluster, pass `--require-live=false --surface custom --url <url>`. Production
load reports must keep `--require-live=true` and include the merged `--revision`.

This HTTP task covers the company-site, Harbor registry API, Dashboard, and
OpenBao health surfaces. Harbor registry write/read load uses
`aspect infra load-harbor-registry`. Postgres/CNPG and ClickHouse database-path
load uses standard database clients through `aspect infra load-db`.

## Database Load Drills

Use `aspect infra load-db` for database-path load against Cozystack-managed
Postgres and ClickHouse. The task builds repo-pinned `kubectl`, runs the same
guardian-mgmt kubeconfig guard as `aspect infra live`, optionally verifies a
merged Flux `--revision`, and creates one ad-hoc Kubernetes `Job` in the tenant
namespace. The job is not part of steady desired state.

The Postgres drill runs `pgbench` from the digest-pinned
`ghcr.io/cloudnative-pg/postgresql:18.1` image, connects to
`Service/postgres-guardian-rw`, creates a temporary database, initializes it
with the requested scale, runs the benchmark, and drops the temporary database
on exit. Credentials come from Cozystack's `postgres-guardian-superuser`
Secret.

```sh
aspect infra load-db \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --stage gamma \
  --component postgres \
  --pgbench-scale 10 \
  --pgbench-clients 4 \
  --pgbench-jobs 2 \
  --pgbench-duration-seconds 120
```

The ClickHouse drill runs `clickhouse-benchmark` from the digest-pinned
`clickhouse/clickhouse-server:24.9.2.42` image, connects to
`Service/chendpoint-clickhouse-guardian`, and uses the Cozystack backup user's
password from `Secret/clickhouse-guardian-credentials`.

```sh
aspect infra load-db \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --stage gamma \
  --component clickhouse \
  --clickhouse-concurrency 4 \
  --clickhouse-iterations 100 \
  --clickhouse-duration-seconds 120 \
  --clickhouse-query 'SELECT sum(number) FROM numbers(1000000)'
```

The report input is standard Kubernetes `Job` YAML, related pods, and pod logs
containing the native `pgbench` or `clickhouse-benchmark` output. Do not wrap it
in a Guardian-specific evidence format, and do not check one-shot load Jobs into
Flux.

## Backup And Restore Drills

Ad-hoc backup/restore evidence should use Cozystack's native
`BackupJob`, `Backup`, and `RestoreJob` resources. Do not check one-shot
`BackupJob` resources into the Flux path; they are operation records, not
steady desired state.

`aspect infra backup-drill` is a thin wrapper around repo-pinned `kubectl` and a
small repo-built helper. It first runs the same guardian-mgmt kubeconfig guard
as `aspect infra live`, then creates a one-shot `BackupJob`, waits for
`BackupJob.status.phase=Succeeded`, waits for the resulting
`Backup.status.phase=Ready`, and prints standard Kubernetes resource YAML,
related Jobs/Pods, and pod logs where the backup strategy labels them.
If `--name` is omitted, the helper generates a unique UTC timestamped
`BackupJob` name.

Run a ClickHouse backup smoke drill with:

```sh
aspect infra backup-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --stage dev \
  --component clickhouse
```

Run a restore drill only into an existing restore target app, not the serving
app:

```sh
aspect infra backup-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --stage dev \
  --component clickhouse \
  --restore-target guardian-restore
```

The helper refuses in-place restore by default. `--allow-in-place-restore=true`
exists for an intentional repair operation, not for routine drills.

The same command supports `--component postgres`, but Postgres backup drills
will not pass until the corresponding `Postgres/guardian` app has
`spec.backup.enabled`, `destinationPath`, and `endpointURL` wired to declared
non-secret R2 coordinates. The reusable CNPG `BackupClass` and OpenBao-projected
credential Secrets already exist.

For PR-local evidence, capture the unmodified command output. Durable reports
should summarize the standard Kubernetes objects and include the relevant
`BackupJob`, `Backup`, `RestoreJob`, and strategy pod log excerpts. Do not add
repo-specific JSON evidence bundles.

## OpenBao Drills

OpenBao is intentionally different from Postgres, Harbor, and ClickHouse: if
the entire management cluster is lost, recovering the old OpenBao key material
is not required. Rebuild the cluster, re-initialize OpenBao, re-apply
`src/infrastructure/bootstrap/guardian-mgmt-openbao`, and repopulate the
required kv-v2 paths from the operator's secret source. For single-pod or
single-node failures inside an otherwise surviving cluster, the cluster-local
bootstrap Secret is enough to unseal replicas again.

`aspect infra openbao-drill` is a thin wrapper around repo-pinned `kubectl` and
native `bao` commands executed inside the Cozystack OpenBao pods. It first runs
the same guardian-mgmt kubeconfig guard as `aspect infra live`, and when
`--revision` is provided it verifies source-controller convergence before any
mutation.

Print status for all OpenBao replicas:

```sh
aspect infra openbao-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --mode status
```

Initialize OpenBao if needed and unseal all replicas:

```sh
aspect infra openbao-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --mode init-unseal
```

When `mode=init-unseal` initializes a fresh OpenBao cluster, it runs
`bao operator init -key-shares=1 -key-threshold=1 -format=json`, captures the
native JSON output without printing the root token or unseal key, and writes a
cluster-local `Secret/openbao-guardian-bootstrap` in `tenant-root`. That Secret
is operational bootstrap material, not offsite survival state. Do not check its
contents into Git.

Run an OpenBao Raft snapshot smoke drill:

```sh
aspect infra openbao-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --mode snapshot
```

The snapshot drill runs `bao operator raft autopilot state`, then
`bao operator raft snapshot save` to a pod-local `/tmp` file, verifies the file
is non-empty with `sha256sum`, and removes the pod-local snapshot. The report
input is the native `bao`, `kubectl`, and `sha256sum` output.

## Single Node Outage Drills

Use the Kubernetes eviction path first. `kubectl drain` is the standard
maintenance primitive here because it respects `PodDisruptionBudget` objects and
fails instead of bypassing an unsafe topology.

`aspect infra node-outage-drill` is a thin wrapper around repo-pinned `kubectl`
and a small repo-built helper. It first runs the same guardian-mgmt kubeconfig
guard as `aspect infra live`, then prints node, pod, PDB, app, and dashboard
status, cordons and drains the selected node, prints the same status while the
node is drained, uncordons the node, and waits for recovery. The recovery gate
requires the target node to be `Ready`, the dashboard deployments to be
`Available`, OpenBao to be ready with three statefulset replicas, root, dev,
gamma, and prod Postgres, Harbor, and ClickHouse apps to report `Ready` and
`WorkloadsReady`, and the dev/gamma/prod company-site deployments to be
`Available`.

Run it against one node at a time:

```sh
aspect infra node-outage-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --node ash-earth \
  --confirm-node ash-earth
```

`--confirm-node` must exactly match `--node`; this is a deliberate guard because
the task mutates scheduling state. The helper does not pass `--force` or
`--disable-eviction` to `kubectl drain`, so PDB failures, unmanaged pods, or
other eviction problems stop the drill. If the drain or recovery checks fail
after cordon, the helper best-effort uncordons the node before exiting.

Capture the unmodified command output for PR-local evidence. Durable outage
reports should summarize the standard `kubectl` output and the relevant PDB,
pod placement, Cozystack app conditions, dashboard deployment conditions, and
company-site deployment conditions. Hard power-loss, Talos reboot, and provider
power-cycle drills are separate exercises once the current Talos operator state
is trustworthy.

## Company Site

The active company-site artifact is the TanStack Start/Nitro OCI image exposed
through `//src/products/company/site:image`. The deploy-facing `site` package
is a compatibility shim over `//src/products/company/web:image`; the web app
source, Vite+ workspace, pinned pnpm lock, and brand package now live under
`src/`. The runtime image uses the digest-pinned Ubuntu base from
`MODULE.bazel`, the repo-pinned Node toolchain, and exposes `/healthz`,
`/livez`, `/metrics`, and the public company routes.
The archived `src-old/` tree is not a source of company-site build inputs.

The OG endpoints currently serve generated SVG cards. If PNG social-card
compatibility becomes required, add it as a pre-rendered build artifact instead
of reintroducing a native rasterizer in the request path.

Build the image with:

```sh
aspect build //src/products/company/site:image
```

Publish the image to the root Harbor registry after Harbor is reconciled:

```sh
aspect run //src/products/company/site:push-harbor
```

The checked-in environment layer declares:

- `tenant-dev`: `Deployment`, `Service`, `PodDisruptionBudget`, and `Ingress`
  for `dev.gi.org`.
- `tenant-gamma`: `Deployment`, `Service`, `PodDisruptionBudget`, and
  `Ingress` for `gamma.gi.org`.
- `tenant-prod`: `Deployment`, `Service`, `PodDisruptionBudget`, and
  `Ingress` for `guardianintelligence.org`.

Each deployment runs three replicas, uses the `tenant-root` ingress class, and
references the immutable Harbor image digest produced by the checked-in
TanStack artifact. Containers run as uid/gid `65532`, drop all Linux
capabilities, and use a read-only root filesystem with only `/tmp` backed by an
`emptyDir` scratch volume. Pods use a strict hostname topology spread
(`maxSkew: 1`, `whenUnsatisfiable: DoNotSchedule`), and each environment
declares `PodDisruptionBudget/company-site` with `minAvailable: 2` so
voluntary disruption cannot take the surface below the single-node-outage
target.
Each environment also declares `NetworkPolicy/company-site-ingress`, selecting
only the company-site pods and allowing inbound TCP/8080 traffic only from the
`tenant-root` ingress-nginx controller pods. Egress remains unrestricted until
the site has a declared in-cluster telemetry collector endpoint.
The same-origin OTLP browser forwarder requires
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT`; without
one it returns `503` instead of falling back to localhost or an undeclared
sidecar.

## Live Checks

Run these after Flux has reconciled the base:

```sh
kubectl get nodes -o wide
kubectl get subnet ovn-default join -o custom-columns=NAME:.metadata.name,MTU:.spec.mtu
kubectl -n cozy-metallb get ipaddresspool,l2advertisement
kubectl -n cozy-fluxcd get gitrepository,kustomization
kubectl get packages.cozystack.io cozystack.backup-controller cozystack.backupstrategy-controller
kubectl -n cozy-backup-controller get helmrelease backup-controller backupstrategy-controller
kubectl -n cozy-backup-controller get deployment backup-controller backupstrategy-controller
kubectl -n cozy-backup-controller get serviceaccount backup-controller backupstrategy-controller
kubectl get clusterrole backups.cozystack.io:core-controller backups.cozystack.io:strategy-controller
kubectl get clusterrolebinding backups.cozystack.io:core-controller backups.cozystack.io:strategy-controller
kubectl get crd backupclasses.backups.cozystack.io backupjobs.backups.cozystack.io backups.backups.cozystack.io plans.backups.cozystack.io restorejobs.backups.cozystack.io
kubectl get crd jobs.strategy.backups.cozystack.io cnpgs.strategy.backups.cozystack.io altinities.strategy.backups.cozystack.io
kubectl wait --for=condition=Ready packages.cozystack.io/cozystack.backup-controller packages.cozystack.io/cozystack.backupstrategy-controller
kubectl -n cozy-backup-controller wait --for=condition=Ready helmrelease/backup-controller helmrelease/backupstrategy-controller
kubectl -n cozy-backup-controller wait --for=condition=Available deployment/backup-controller deployment/backupstrategy-controller
kubectl get storageclass
kubectl -n tenant-root get tenants.apps.cozystack.io
kubectl -n tenant-root get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl get ns tenant-dev tenant-gamma tenant-prod \
  -o custom-columns=NAME:.metadata.name,HOST:.metadata.labels.namespace\\.cozystack\\.io/host,INGRESS:.metadata.labels.namespace\\.cozystack\\.io/ingress
kubectl -n tenant-dev get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl -n tenant-gamma get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl -n tenant-prod get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl -n cozy-dashboard get deployment/cozy-dashboard-console deployment/incloud-web-gatekeeper
kubectl -n cozy-dashboard get service/cozy-dashboard-console service/incloud-web-gatekeeper ingress/dashboard-web-ingress
kubectl -n tenant-root wait --for=condition=Ready tenants.apps.cozystack.io/dev tenants.apps.cozystack.io/gamma tenants.apps.cozystack.io/prod
kubectl -n tenant-root wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian openbaos.apps.cozystack.io/guardian
kubectl -n tenant-root wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-dev wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-dev wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-gamma wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-gamma wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-prod wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-prod wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-dev get deploy,svc,poddisruptionbudget,ingress company-site
kubectl -n tenant-dev get networkpolicy company-site-ingress
kubectl -n tenant-gamma get deploy,svc,poddisruptionbudget,ingress company-site
kubectl -n tenant-gamma get networkpolicy company-site-ingress
kubectl -n tenant-prod get deploy,svc,poddisruptionbudget,ingress company-site
kubectl -n tenant-prod get networkpolicy company-site-ingress
kubectl -n tenant-root get openbao guardian
kubectl -n tenant-root get helmrelease openbao-guardian
kubectl -n tenant-root get helmrelease openbao-guardian-system
kubectl -n tenant-root get service openbao-guardian
kubectl -n tenant-root get statefulset openbao-guardian
kubectl -n tenant-root get ciliumnetworkpolicy allow-openbao-to-apiserver
kubectl -n tenant-root get ciliumnetworkpolicy allow-external-secrets-to-openbao
kubectl -n tenant-root get secretstores.external-secrets.io openbao
kubectl -n tenant-root get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-root get secret guardian-cnpg-backup-creds
kubectl -n tenant-root get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-root get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-root get secret guardian-clickhouse-backup-creds
kubectl -n tenant-root get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-dev get secretstores.external-secrets.io openbao
kubectl -n tenant-dev get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-dev get secret guardian-cnpg-backup-creds
kubectl -n tenant-dev get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-dev get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-dev get secret guardian-clickhouse-backup-creds
kubectl -n tenant-dev get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-gamma get secretstores.external-secrets.io openbao
kubectl -n tenant-gamma get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-gamma get secret guardian-cnpg-backup-creds
kubectl -n tenant-gamma get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-gamma get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-gamma get secret guardian-clickhouse-backup-creds
kubectl -n tenant-gamma get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-prod get secretstores.external-secrets.io openbao
kubectl -n tenant-prod get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-prod get secret guardian-cnpg-backup-creds
kubectl -n tenant-prod get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-prod get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-prod get secret guardian-clickhouse-backup-creds
kubectl -n tenant-prod get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl get cnpgs.strategy.backups.cozystack.io guardian-postgres-r2
kubectl get backupclasses.backups.cozystack.io guardian-postgres-cnpg
kubectl get altinities.strategy.backups.cozystack.io guardian-clickhouse-altinity
kubectl get backupclasses.backups.cozystack.io guardian-clickhouse-altinity
kubectl -n tenant-root wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-root wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
kubectl -n tenant-dev wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-dev wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
kubectl -n tenant-gamma wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-gamma wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
kubectl -n tenant-prod wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-prod wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
kubectl -n tenant-root wait --for=condition=Ready helmrelease/openbao-guardian helmrelease/openbao-guardian-system
kubectl -n tenant-root wait --for=jsonpath='{.status.readyReplicas}'=3 statefulset/openbao-guardian
kubectl -n tenant-root get helmrelease harbor-guardian harbor-guardian-system
kubectl -n tenant-root get bucketclaims.objectstorage.k8s.io harbor-guardian-registry
kubectl -n tenant-root get bucketaccesses.objectstorage.k8s.io harbor-guardian-registry
kubectl -n tenant-root get secret harbor-guardian-registry-bucket harbor-guardian-registry-s3
kubectl -n tenant-root get workloadmonitors.cozystack.io harbor-guardian-core harbor-guardian-registry harbor-guardian-portal
kubectl -n tenant-root get helmrelease postgres-guardian clickhouse-guardian
kubectl -n tenant-root get clusters.postgresql.cnpg.io postgres-guardian
kubectl -n tenant-root get clickhouseinstallations.clickhouse.altinity.com clickhouse-guardian
kubectl -n tenant-root get clickhousekeeperinstallations.clickhouse-keeper.altinity.com clickhouse-guardian-keeper
kubectl -n tenant-root get vmpodscrapes.operator.victoriametrics.com clickhouse-guardian-keeper
kubectl -n tenant-root get workloadmonitors.cozystack.io postgres-guardian clickhouse-guardian clickhouse-guardian-keeper
```

Run the Harbor, Postgres, and ClickHouse child-resource checks in `tenant-dev`,
`tenant-gamma`, and `tenant-prod` as well. `aspect infra live` performs those
namespace repetitions automatically and also checks selected CR spec fields.

Expected results:

- all three nodes are Ready and use `10.8.0.0/24` for internal node addresses
- `Package/cozystack.cozystack-platform` uses variant `isp-full`
- `Package/cozystack.backup-controller` and
  `Package/cozystack.backupstrategy-controller` report `Ready=True`, their
  HelmReleases and deployments are ready in `cozy-backup-controller`, their
  service accounts are bound to the expected cluster roles, and the core
  `backups.cozystack.io` plus Guardian-used `strategy.backups.cozystack.io`
  CRDs exist
- `ovn-default` and `join` report MTU `1362`
- MetalLB has the `cozystack` pool, L2 advertisement, and
  `10.8.0.200-10.8.0.240` address range
- Flux `guardian-mgmt-base` reconciles `src/infrastructure/base`, and
  `guardian-mgmt-tenant-apps` reconciles `src/infrastructure/environments`
- storage classes include `local`, `local-retain`, `replicated`, and
  `replicated-retain`; `replicated` is the only default class and has LINSTOR
  `autoPlace=3`
- root app resources exist for `Postgres/guardian`, `Harbor/guardian`, and
  `ClickHouse/guardian` in `tenant-root`; Postgres is replicated three ways on
  `replicated` storage with version `v18` and no external access, Harbor uses
  the expected host, replicated storage, three database replicas, three Redis
  replicas, and Trivy scanning, and ClickHouse uses three replicas, replicated
  storage, three keeper replicas, the expected backup Secret, and
  `Plan/guardian-clickhouse-daily` through
  `BackupClass/guardian-clickhouse-altinity`
- tenant namespaces exist for dev, gamma, and prod; their host labels are
  `dev.gi.org`, `gamma.gi.org`, and `prod.gi.org`, and their ingress label is
  `tenant-root`
- each tenant namespace has `Postgres/guardian`, `Harbor/guardian`, and
  `ClickHouse/guardian` with the declared three-replica HA shape, replicated
  storage, expected Harbor host, Trivy enabled, ClickHouse backup Secret, and
  environment-specific daily ClickHouse backup Plan schedule
- tenant and service app resources report `Ready=True`
- root/dev/gamma/prod Postgres, Harbor, and ClickHouse app resources report
  `WorkloadsReady=True`; OpenBao app `Ready=True` and
  `StatefulSet/openbao-guardian` has three ready replicas
- root/dev/gamma/prod each have `HelmRelease/harbor-guardian`, nested
  `HelmRelease/harbor-guardian-system`,
  `BucketClaim/harbor-guardian-registry`,
  `BucketAccess/harbor-guardian-registry`,
  `Secret/harbor-guardian-registry-bucket`,
  `Secret/harbor-guardian-registry-s3`, and the Harbor core, registry, and
  portal `WorkloadMonitor` objects. The live gate also verifies that the COSI
  BucketClaim and BucketAccess use protocol `s3`, that the access object points
  at `harbor-guardian-registry`, and that it writes credentials to
  `harbor-guardian-registry-bucket`.
- root/dev/gamma/prod each have `HelmRelease/postgres-guardian`,
  `Cluster/postgres-guardian`, `Secret/postgres-guardian-credentials`,
  `Secret/postgres-guardian-init-script`, and
  `WorkloadMonitor/postgres-guardian`. The live gate verifies the CNPG Cluster
  has three instances, `replicated` storage, and synchronous replica bounds
  `1..2`.
- root/dev/gamma/prod each have `HelmRelease/clickhouse-guardian`,
  `ClickHouseInstallation/clickhouse-guardian`,
  `ClickHouseKeeperInstallation/clickhouse-guardian-keeper`,
  `VMPodScrape/clickhouse-guardian-keeper`,
  `Secret/clickhouse-guardian-credentials`,
  `Secret/clickhouse-guardian-backup-api-auth`, and ClickHouse plus keeper
  WorkloadMonitors. The live gate verifies one shard, three replicas, three
  keeper replicas, and that the ClickHouseInstallation pod template includes
  the `clickhouse-backup` sidecar exposing the `ch-backup-api` port.
- each tenant namespace has the company-site `Deployment`, `Service`,
  `NetworkPolicy`, `PodDisruptionBudget`, and `Ingress`; the dev and gamma
  ingress hosts are `dev.gi.org` and `gamma.gi.org`, and prod is
  `guardianintelligence.org`; each live company-site surface runs the declared
  Harbor digest and has pods placed on three distinct Kubernetes nodes
- OpenBao is deployed as the Cozystack-managed `guardian` app in `tenant-root`
  with `HelmRelease/openbao-guardian`, nested
  `HelmRelease/openbao-guardian-system`, `Service/openbao-guardian`, and
  `StatefulSet/openbao-guardian` with three ready replicas
- `tenant-root` has the Cilium allow policies for OpenBao-to-API-server
  traffic and ESO-to-OpenBao traffic
- after OpenBao has been initialized/unsealed, the OpenTofu OpenBao root has
  been applied, and the matching kv-v2 values exist, root/dev/gamma/prod have
  Ready `SecretStore/openbao`, Ready
  `ExternalSecret/guardian-cnpg-backup-creds`, and the target
  `Secret/guardian-cnpg-backup-creds`; the live gate also verifies that each
  CNPG backup SecretStore points at
  `http://openbao-guardian.tenant-root.svc:8200`, uses the `kv` v2 engine, the
  `kubernetes` auth mount, the expected tenant-scoped role, the
  `guardian-external-secrets` service account, and the `openbao` audience
- after the same OpenBao prerequisites, root/dev/gamma/prod have Ready
  `SecretStore/openbao-clickhouse-backup`, Ready
  `ExternalSecret/guardian-clickhouse-backup-creds`, and the target
  `Secret/guardian-clickhouse-backup-creds`; the live gate verifies the same
  OpenBao provider fields with the tenant-scoped ClickHouse backup role and the
  `guardian-clickhouse-external-secrets` service account
- the cluster has `CNPG/guardian-postgres-r2` and
  `BackupClass/guardian-postgres-cnpg`, plus
  `Altinity/guardian-clickhouse-altinity` and
  `BackupClass/guardian-clickhouse-altinity`
- root/dev/gamma/prod each have
  `Plan/guardian-clickhouse-daily` targeting `ClickHouse/guardian` through
  `BackupClass/guardian-clickhouse-altinity`

Talos-side network checks are handled by:

```sh
aspect infra talos
```

The task uses repo-pinned `talosctl`, reads
`src/infrastructure/talm/talosconfig`, checks that all three management nodes
report their `10.8.0.x` addresses, verifies routes can be read from the same
Talos endpoints, and requires KubeSpan peer status to be empty.

Kubernetes-side readiness evidence should be captured as PR-local command output
while a change is being reviewed. Durable operational proof should come from
standard tools already in the stack: Flux status, Kubernetes conditions,
Cozystack backup/restore resources, load-test tool output, and monitoring data.
Do not add repo-specific JSON evidence bundles or durable CLI/task surfaces whose
only purpose is temporary PR verification.

## Not Done In This Substrate Slice

These are intentionally outside the merged L2/OpenTofu substrate and need
separate PRs with their own validation:

- Bootstrap CLI wrapper for the full Talm/Talos path.
- Latitude VLAN assignment imports, once assignment IDs are collected.
- Publishing the checked-in TanStack company-site OCI image to Harbor and
  capturing live readiness evidence for dev, gamma, and prod.
- Live load-test reports for CNPG/Postgres, Harbor, ClickHouse, OpenBao, the
  Cozystack dashboard, and the company-site surfaces. `aspect infra load-http`
  provides the standard k6 path for HTTP-facing Harbor, OpenBao health,
  dashboard, and company-site reports. `aspect infra load-harbor-registry`
  provides the standard ORAS push/pull path for Harbor registry data-path
  reports. `aspect infra load-db` provides the standard `pgbench` and
  `clickhouse-benchmark` path for Postgres/CNPG and ClickHouse reports. Live
  reports still require current guardian-mgmt credentials and successful
  source-controller convergence on the merged revision.
- Postgres backup specs wired to declared OpenBao/R2-projected Secrets, plus
  live backup/restore drills for Postgres, Harbor, and ClickHouse. The package
  prerequisites, backup controller deployments/RBAC/CRDs, reusable CNPG
  BackupClass, reusable ClickHouse Altinity BackupClass, Postgres / ClickHouse
  credential SecretStores and ExternalSecrets, OpenBao auth/policy
  configuration, ClickHouse app backup Secret references, recurring ClickHouse
  backup Plans, and Harbor's COSI-backed registry bucket resources are declared
  and live-gated. `aspect infra backup-drill` now creates ad-hoc Cozystack
  BackupJob/RestoreJob resources and prints standard Kubernetes evidence, but
  applying the OpenBao root, populating real kv values, Postgres object-store
  coordinates, running the smoke tests, Harbor registry restore validation, and
  live restore drill reports still need separate PRs.
- ClickHouse chart-side `spec.storageClass` rendering, because Cozystack 1.4
  still relies on the cluster default for ClickHouse and keeper PVCs.
- Live OpenBao init/unseal and Raft snapshot drill reports from
  `aspect infra openbao-drill`. The task surface is declared and validated, but
  live reports still require current guardian-mgmt credentials and
  source-controller convergence on the merged revision.
- Load-test and hard disaster-recovery drills for each new infrastructure
  component, plus live single-node outage reports from
  `aspect infra node-outage-drill`, recorded through standard tool outputs
  rather than a Guardian-specific evidence schema.
