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
with that HCL. Change the HCL and dependent manifests together; do not let
topology drift through one-off edits.

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
Postgres/Harbor/ClickHouse workloads.

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
package install.

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
`http://guardian.tenant-root.svc:8200`, use the `kv` engine with `version: v2`,
and authenticate through the `kubernetes` auth mount with audience `openbao`.
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

## Company Site

The active company-site artifact is the static OCI image at
`//src/products/company/site:image`. It uses the digest-pinned
`nginx-unprivileged` base from `MODULE.bazel`, serves only checked-in static
files, and exposes `/healthz`, `/livez`, and `/metrics`.

Build the image with:

```sh
bazelisk build //src/products/company/site:image
```

Publish the image to the root Harbor registry after Harbor is reconciled:

```sh
bazelisk run //src/products/company/site:push-harbor
```

The checked-in environment layer declares:

- `tenant-dev`: `Deployment`, `Service`, and `Ingress` for `dev.gi.org`.
- `tenant-gamma`: `Deployment`, `Service`, and `Ingress` for `gamma.gi.org`.
- `tenant-prod`: `Deployment`, `Service`, and `Ingress` for
  `guardianintelligence.org`.

Each deployment runs three replicas, uses the `tenant-root` ingress class, and
references the immutable Harbor image digest produced by the checked-in static
artifact. The full TanStack company site remains archived under `src-old/` until
its pinned JS workspace is restored as an active build graph slice.

## Live Checks

Run these after Flux has reconciled the base:

```sh
kubectl get nodes -o wide
kubectl get subnet ovn-default join -o custom-columns=NAME:.metadata.name,MTU:.spec.mtu
kubectl -n cozy-metallb get ipaddresspool,l2advertisement
kubectl -n cozy-fluxcd get gitrepository,kustomization
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
kubectl -n tenant-dev get deploy,svc,ingress company-site
kubectl -n tenant-gamma get deploy,svc,ingress company-site
kubectl -n tenant-prod get deploy,svc,ingress company-site
kubectl -n tenant-root get openbao guardian
kubectl -n tenant-root get ciliumnetworkpolicy allow-external-secrets-to-openbao
kubectl -n tenant-root get secretstores.external-secrets.io openbao
kubectl -n tenant-root get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-root get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-root get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-root get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-dev get secretstores.external-secrets.io openbao
kubectl -n tenant-dev get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-dev get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-dev get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-dev get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-gamma get secretstores.external-secrets.io openbao
kubectl -n tenant-gamma get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-gamma get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-gamma get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-gamma get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-prod get secretstores.external-secrets.io openbao
kubectl -n tenant-prod get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-prod get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-prod get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-prod get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl get cnpgs.strategy.backups.cozystack.io guardian-postgres-r2
kubectl get backupclasses.backups.cozystack.io guardian-postgres-cnpg
kubectl get altinities.strategy.backups.cozystack.io guardian-clickhouse-altinity
kubectl get backupclasses.backups.cozystack.io guardian-clickhouse-altinity
```

Expected results:

- all three nodes are Ready and use `10.8.0.0/24` for internal node addresses
- `ovn-default` and `join` report MTU `1362`
- MetalLB has the `cozystack` pool and L2 advertisement
- Flux `guardian-mgmt-base` reconciles `src/infrastructure/base`, and
  `guardian-mgmt-tenant-apps` reconciles `src/infrastructure/environments`
- storage classes include `local`, `local-retain`, `replicated`, and
  `replicated-retain`; `replicated` is the only default class
- root app resources exist for `Postgres/guardian`, `Harbor/guardian`, and
  `ClickHouse/guardian` in `tenant-root`
- tenant namespaces exist for dev, gamma, and prod; their host labels are
  `dev.gi.org`, `gamma.gi.org`, and `prod.gi.org`, and their ingress label is
  `tenant-root`
- each tenant namespace has `Postgres/guardian`, `Harbor/guardian`, and
  `ClickHouse/guardian`
- tenant and service app resources report `Ready=True`
- root/dev/gamma/prod Postgres, Harbor, and ClickHouse app resources report
  `WorkloadsReady=True`; OpenBao app `Ready=True` is sufficient until
  init/unseal is declared in the bootstrap path
- each tenant namespace has the company-site `Deployment`, `Service`, and
  `Ingress`; the dev and gamma ingress hosts are `dev.gi.org` and
  `gamma.gi.org`, and prod is `guardianintelligence.org`
- OpenBao is deployed as the Cozystack-managed `guardian` app in `tenant-root`
- `tenant-root` has the Cilium allow policy for ESO-to-OpenBao traffic
- root/dev/gamma/prod have `SecretStore/openbao` and
  `ExternalSecret/guardian-cnpg-backup-creds`; they do not have to be Ready
  until OpenBao has been initialized/unsealed and populated with the matching
  roles and kv-v2 values
- root/dev/gamma/prod have `SecretStore/openbao-clickhouse-backup` and
  `ExternalSecret/guardian-clickhouse-backup-creds`; they do not have to be
  Ready until OpenBao has been initialized/unsealed and populated with the
  matching roles and kv-v2 values
- the cluster has `CNPG/guardian-postgres-r2` and
  `BackupClass/guardian-postgres-cnpg`, plus
  `Altinity/guardian-clickhouse-altinity` and
  `BackupClass/guardian-clickhouse-altinity`
- root/dev/gamma/prod each have
  `Plan/guardian-clickhouse-daily` targeting `ClickHouse/guardian` through
  `BackupClass/guardian-clickhouse-altinity`

Talos-side network checks:

```sh
talosctl --nodes 10.8.0.11,10.8.0.12,10.8.0.13 --endpoints 10.8.0.250 get addresses
talosctl --nodes 10.8.0.11,10.8.0.12,10.8.0.13 --endpoints 10.8.0.250 get routes
talosctl --nodes 10.8.0.11,10.8.0.12,10.8.0.13 --endpoints 10.8.0.250 get kubespanpeerstatuses
```

Expected result: addresses and routes show the VLAN subnet, and KubeSpan has no
active mesh peers.

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
- Publishing the checked-in company-site OCI image to Harbor and capturing live
  readiness evidence for dev, gamma, and prod.
- Load-test reports for CNPG/Postgres, Harbor, ClickHouse, OpenBao, the
  Cozystack dashboard, and the company-site surfaces.
- Backup specs for root and environment Postgres/Harbor, wired to declared
  OpenBao/R2-projected Secrets. The package prerequisites, reusable CNPG
  BackupClass, reusable ClickHouse Altinity BackupClass, Postgres / ClickHouse
  credential SecretStores and ExternalSecrets, OpenBao auth/policy
  configuration, ClickHouse app backup Secret references, and recurring
  ClickHouse backup Plans are declared. Applying the OpenBao root, populating
  real kv values, Postgres object-store coordinates, Harbor backup strategy,
  ad-hoc BackupJob smoke tests, and live restore drills still need separate
  PRs.
- ClickHouse chart-side `spec.storageClass` rendering, because Cozystack 1.4
  still relies on the cluster default for ClickHouse and keeper PVCs.
- OpenBao init/unseal automation and backup/restore drills.
- Load-test, disaster-recovery, and single-node-outage drills for each new
  infrastructure component, recorded through standard tool outputs rather than
  a Guardian-specific evidence schema.
