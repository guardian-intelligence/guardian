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
| Public DNS records | `src/infrastructure/bootstrap/guardian-mgmt-dns/*.tf` |
| Talos/Talm chart | `src/infrastructure/talm/` |
| Cozystack platform package | `src/infrastructure/base/cozystack/platform.yaml` |
| Core Cozystack apps | `src/infrastructure/base/apps/core-services.yaml` |
| Backup strategy classes | `src/infrastructure/base/backup/` |
| OpenBao-backed secret delivery | `src/infrastructure/base/backup/root-*-backup-secrets.yaml`, `src/infrastructure/products/platform/*/secrets.yaml` |
| MetalLB L2 pool | `src/infrastructure/base/networking/metallb.yaml` |
| Kube-OVN MTU | `src/infrastructure/base/networking/subnet-mtu.yaml` |
| Flux handoff | `src/infrastructure/base/flux/sync.yaml` |
| OpenBao app | `src/infrastructure/base/openbao/` |
| LINSTOR storage | `src/infrastructure/base/storage/` |
| Product tenant topology | `src/infrastructure/tenants/guardian-commercial/`, `src/infrastructure/tenants/platform/` |
| Product stage Cozystack apps | `src/infrastructure/products/platform/` |
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
  checked-in `data` pool. The pool devices are declared by stable
  `/dev/disk/by-id/nvme-...` identities and must stay on the non-Talos install
  disk for each node; Latitude NVMe kernel names can swap across boots. `local`
  and `local-retain` remain available only for explicitly selected scratch or
  intentionally node-local state.

## Validate Checked-In Substrate

The repo pins OpenTofu in `MODULE.bazel`, the Latitude provider in
`src/infrastructure/bootstrap/guardian-mgmt/.terraform.lock.hcl`, the Vault
provider in `src/infrastructure/bootstrap/guardian-mgmt-openbao/.terraform.lock.hcl`,
and the AWS/Cloudflare providers in
`src/infrastructure/bootstrap/guardian-mgmt-dns/.terraform.lock.hcl`.

Run the full local substrate check with:

```sh
aspect infra validate
```

`aspect infra validate` runs OpenTofu `fmt`/`init -backend=false`/`validate` for
the three bootstrap roots, builds the infrastructure filegroups, runs the active
manifest invariant test, and renders both Kustomize roots with the repo-pinned
kubectl artifact.

Run the active invariant test directly with:

```sh
bazelisk test //src/infrastructure/tests:manifest_invariants_test
```

That test parses the checked-in Kubernetes YAML and uses the repo-pinned `talm`
binary to render the management control-plane template offline with throwaway
secrets under the Bazel test temp directory. It verifies the platform package
publishes the dashboard/API endpoints, product stage tenants use the expected
`*.gi.org` hosts, OpenTofu/Talm/Kubernetes manifests stay aligned with the
management OpenTofu topology, the rendered Talos config keeps the L2 VIP and
VLAN control-plane path, KubeSpan/WireGuard stays absent, MetalLB and Kube-OVN
keep the L2/MTU topology, `replicated` is the only default StorageClass, the
Piraeus/LINSTOR `data` pool is declared for all three management nodes, root
Ingress provides tenant-root ingress, root SeaweedFS provides tenant-root object
storage, root and environment Postgres/Harbor/ClickHouse apps use the intended
HA/storage shape, OpenBao stays declared in `tenant-root`, the OpenBao OpenTofu
root declares only mounts, policies, and Kubernetes-auth roles, the reusable
CNPG backup strategy maps through a cluster-scoped BackupClass, OpenBao-backed
CNPG backup credential projections exist for root/dev/gamma/prod, the company
site is declared for dev/gamma/prod, and Flux reconciles base before tenant
apps.

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
the VLAN VIP using the first node from the comma-separated management node list
and writes `src/infrastructure/talm/kubeconfig`. If the Talos CA itself was
rotated and the local Talm secrets are stale, restore or regenerate the operator
state through the bootstrap path instead. Do not use insecure TLS flags for
source-controller validation.
The task checks for `src/infrastructure/talm/secrets.yaml`,
`src/infrastructure/talm/talm.key`, and
`src/infrastructure/talm/talosconfig.encrypted` before invoking Talm, so a
partial or missing operator-state restore fails before generating fresh local
key material in the repo.

## Management Bootstrap Command

The current repo-native bootstrap surface is:

```sh
aspect infra tofu-init

aspect infra bootstrap --revision "<merged-main-commit-sha>"

aspect infra openbao-drill \
  --mode init-unseal \
  --revision "<merged-main-commit-sha>"

aspect infra openbao-apply \
  --revision "<merged-main-commit-sha>"

aspect infra openbao-backup-secrets \
  --revision "<merged-main-commit-sha>"
```

It is intentionally a thin orchestration path over standard tools and existing
repo tasks. It initializes the standard OpenTofu S3 backend using the checked-in
Cloudflare account id in `src/infrastructure/bootstrap/backend.tfvars` to derive
the R2 endpoint, while credentials still come from the standard AWS/R2
environment variables. `AWS_ENDPOINT_URL_S3`, `--endpoint`, or
`--tofu-backend-endpoint` can override the derived endpoint when needed. The
task then prints the standard OpenTofu `output -json` for
`src/infrastructure/bootstrap/guardian-mgmt`, runs `aspect infra validate`,
refreshes the gitignored Talm kubeconfig, runs the Talos L2 gate, and then runs
the same live source-controller checks as `aspect infra live`. It does not
define a Guardian-specific inventory format or evidence schema.
OpenBao API configuration is a separate post-app step: initialize/unseal the
Cozystack OpenBao app with `aspect infra openbao-drill --mode init-unseal`, then
run `aspect infra openbao-apply`. The apply task waits for the OpenBao
StatefulSet, reads the cluster-local bootstrap token Secret without printing
secret material, opens a local `kubectl port-forward`, initializes the standard
R2-backed OpenTofu backend, and applies
`src/infrastructure/bootstrap/guardian-mgmt-openbao` with
`openbao_addr=http://127.0.0.1:<port>`.
Backup credential delivery is the next standard OpenBao operation. Run
`aspect infra openbao-backup-secrets` after `openbao-apply`; it reads scoped
backup credentials from
`GUARDIAN_BACKUP_<STAGE>_<COMPONENT>_AWS_ACCESS_KEY_ID` and
`GUARDIAN_BACKUP_<STAGE>_<COMPONENT>_AWS_SECRET_ACCESS_KEY`, writes the
declared kv-v2 paths for root/dev/gamma/prod, annotates the ExternalSecrets
with `force-sync`, and waits for the target Kubernetes Secrets. Use stage names
`ROOT`, `DEV`, `GAMMA`, and `PROD`; use components `POSTGRES` and
`CLICKHOUSE`. The default non-secret coordinates are the checked-in R2 endpoint,
bucket `guardian-vault`, and region `auto`; override them with `--endpoint`,
`--bucket`, or `--region` when the backup bucket differs from the
survival-floor bucket.

Do not use generic `AWS_ACCESS_KEY_ID` or OpenTofu backend credentials as
database backup credentials. A shared pair can be used only as a temporary
bootstrap escape hatch by setting `--allow-shared-backup-credential=true` and
`GUARDIAN_BACKUP_AWS_ACCESS_KEY_ID` /
`GUARDIAN_BACKUP_AWS_SECRET_ACCESS_KEY`; replace it with scoped credentials
before treating backup load or recovery drills as production evidence.

Public DNS is a separate OpenTofu root:
`src/infrastructure/bootstrap/guardian-mgmt-dns`. It manages Route53 records for
`gi.org` and Cloudflare records for `guardianintelligence.org`. The DNS root
does not duplicate node IPs; it reads
`opentofu/guardian-mgmt.tfstate` through standard OpenTofu remote state and uses
the `control_plane_nodes` output from the bare-metal topology root. This keeps
public records aligned with the management-node state and avoids another custom
inventory file.

Use the DNS task to plan or apply records:

```sh
aspect infra dns-apply \
  --mode plan

aspect infra dns-apply \
  --mode apply
```

The task initializes the standard R2-backed state for
`guardian-mgmt-dns`, passes `src/infrastructure/bootstrap/backend.tfvars` as a
normal OpenTofu var-file, and then runs `tofu plan` or `tofu apply
-auto-approve`. DNS credentials stay in standard provider environment variables.

If cert-manager leaves an HTTP-01 `Challenge` pending with a self-check timeout
for `http://<host>/.well-known/acme-challenge/...`, verify public DNS before
touching the cluster. The ACME solver path should return `200` when resolved
directly to each current ingress IP, and the public record should resolve to
the same IP set:

```sh
getent ahostsv4 gamma.gi.org

curl --resolve gamma.gi.org:80:206.223.228.101 \
  http://gamma.gi.org/.well-known/acme-challenge/<token>
```

If the direct solver request works but public DNS still points at old
infrastructure, run `aspect infra dns-apply --mode apply` with the standard
Route53, Cloudflare, and R2 backend credentials. Rebooting or reimaging nodes
does not fix stale public DNS.

The minimal host-come-up CLI delegates to the same task:

```sh
bazelisk run //src/guardian/cmd/guardian -- \
  up management \
  --revision "<merged-main-commit-sha>"
```

Pass explicit state paths only when exercising a non-default Talm root:

```sh
bazelisk run //src/guardian/cmd/guardian -- \
  up management \
  --revision "<merged-main-commit-sha>" \
  --root src/infrastructure/talm \
  --talosconfig src/infrastructure/talm/talosconfig \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG"
```

This command still requires the standard operator prerequisites for the steps it
wraps: OpenTofu backend credentials for `tofu output`, current Talm
operator secrets for kubeconfig refresh, and a trusted guardian-mgmt kubeconfig
for live validation. If any credential is stale, refresh or restore the
standard state for that tool; do not bypass TLS verification.

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

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt-dns" fmt -check

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt-dns" \
  init -backend=false -input=false -reconfigure

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt-dns" validate
```

Live planning requires:

- `LATITUDESH_AUTH_TOKEN` for the Latitude provider.
- S3-compatible backend credentials for R2 through the usual `AWS_*`
  environment variables.
- The checked-in Cloudflare account id in
  `src/infrastructure/bootstrap/backend.tfvars`, unless intentionally overriding
  the derived endpoint with `AWS_ENDPOINT_URL_S3` or a task flag.
- AWS Route53 credentials allowed to manage `gi.org` records, before running
  `aspect infra dns-apply`.
- A Cloudflare API token allowed to read the `guardianintelligence.org` zone and
  edit DNS records, before running `aspect infra dns-apply`.
- A healthy, initialized/unsealed OpenBao app and its cluster-local
  `openbao-guardian-bootstrap` Secret when applying
  `src/infrastructure/bootstrap/guardian-mgmt-openbao` through
  `aspect infra openbao-apply`. The task supplies `VAULT_TOKEN` from that Secret
  to the repo-pinned OpenTofu process without printing it.
- Scoped backup credentials such as
  `GUARDIAN_BACKUP_GAMMA_POSTGRES_AWS_ACCESS_KEY_ID`,
  `GUARDIAN_BACKUP_GAMMA_POSTGRES_AWS_SECRET_ACCESS_KEY`,
  `GUARDIAN_BACKUP_GAMMA_CLICKHOUSE_AWS_ACCESS_KEY_ID`, and
  `GUARDIAN_BACKUP_GAMMA_CLICKHOUSE_AWS_SECRET_ACCESS_KEY`, before running
  `aspect infra openbao-backup-secrets`.

```sh
aspect infra tofu-init \
  --root all

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" plan -input=false

aspect infra openbao-apply \
  --mode plan \
  --revision "<merged-main-commit-sha>"

aspect infra openbao-backup-secrets \
  --dry-run true \
  --revision "<merged-main-commit-sha>"

aspect infra dns-apply \
  --mode plan
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
- tracked per-node Talm patches under `src/infrastructure/talm/nodes/`, each
  using Talos `machine.install.diskSelector.serial` for the system disk

Generated Talm secrets, kubeconfig, and local operator state must stay out of
Git. `src/infrastructure/talm/.gitignore` excludes those paths. The per-node
Talm patches are intentionally checked in because they contain non-secret
physical facts required for unattended rebuilds: stable install disk serials,
public endpoints, and the VLAN address overlay. Do not replace install disk
selectors with `/dev/nvme*`; NVMe names can swap across boots.

The invariant test renders the checked-in control-plane template with the
repo-pinned `talm` artifact and scratch secrets only. The current
`aspect infra bootstrap` / `guardian up management` slice exercises the
already-adopted management cluster path. A later destructive host-come-up slice
still needs to reuse this chart and the repo-pinned `talm`/`talosctl` artifacts
to render each node, apply the first config in Talos maintenance mode,
bootstrap etcd exactly once, and persist the encrypted genesis bundle under
operator state.

## Kubernetes Handoff

After Talos is bootstrapped and Cozystack is installed, apply the Flux handoff
once:

```sh
kubectl apply -f src/infrastructure/base/flux/sync.yaml
```

Flux reconciles the management cluster in ordered slices:

- `guardian-mgmt-platform` reconciles `src/infrastructure/base/cozystack` and
  applies the Cozystack Platform package.
- `guardian-mgmt-storage` reconciles `src/infrastructure/base/storage` and
  applies the LINSTOR-backed storage classes plus per-node data pools. This
  slice depends on the platform slice and retries while Piraeus CRDs are still
  coming up.
- `guardian-mgmt-base` reconciles `src/infrastructure/base` after storage is
  declared. It applies root Postgres/Harbor/ClickHouse apps, backup strategy
  and BackupClass manifests, backup credential projections, networking
  manifests, OpenBao, and the Flux objects themselves.
- `guardian-mgmt-tenant-apps` keeps the existing live Flux object name and now
  reconciles `src/infrastructure/tenants/guardian-commercial`. This creates the
  `guardiancommercial` product boundary under `tenant-root` and prunes the old
  top-level environment inventory.
- `guardian-mgmt-platform-tenant` reconciles
  `src/infrastructure/tenants/platform` after the commercial boundary exists.
- `guardian-mgmt-platform-<stage>-tenant` reconciles
  `src/infrastructure/tenants/platform/<stage>` and waits for the Cozystack
  tenant controller to create the stage namespace.
- `guardian-mgmt-platform-dev`, `guardian-mgmt-platform-gamma`, and
  `guardian-mgmt-platform-prod` reconcile `src/infrastructure/products/platform/*`
  in dev -> gamma -> prod order. Those product stage wrappers apply the core
  service, company-site, and backup credential projection manifests.

Flux waits on tenant-only slices and health-checks the namespace each tenant
chart creates; namespace creation is the desired readiness signal. Product app
slices use `wait: false` because Cozystack app CRs fan out into HelmReleases and
stateful workloads. Service readiness is proven by the Cozystack app resources'
`Ready` and `WorkloadsReady` conditions plus the component-specific live drill
output, not by treating Flux's apply status as service health.

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

For `Tenant`, the Cozystack source sets `release.prefix: tenant-`, and
Cozystack 1.4 tenant names are lowercase alphanumeric only. Applying
`Tenant/guardiancommercial` in `tenant-root` creates namespace
`tenant-guardiancommercial`; applying `Tenant/platform` there creates
`tenant-guardiancommercial-platform`; applying `Tenant/dev`, `Tenant/gamma`, and
`Tenant/prod` in that product namespace creates the stage namespaces. The stage
tenants intentionally inherit root `etcd`, ingress, monitoring, and SeaweedFS;
they only set explicit stage hostnames for now.

The same source-level naming rule applies to Guardian's core services:
`Postgres/guardian` becomes `HelmRelease/postgres-guardian`,
`Harbor/guardian` becomes `HelmRelease/harbor-guardian`,
`ClickHouse/guardian` becomes `HelmRelease/clickhouse-guardian`, and
`OpenBAO/guardian` becomes `HelmRelease/openbao-guardian`. Root
`Ingress/ingress` and `SeaweedFS/seaweedfs` use application definitions with an
empty release prefix, so their HelmReleases are `ingress` and `seaweedfs` in
`tenant-root`. The Ingress app renders nested `ingress-nginx-system`, which
other tenant-root apps can depend on before creating public Ingress resources.
The SeaweedFS chart renders the standard COSI `BucketClass/tenant-root`,
`BucketClass/tenant-root-lock`, `BucketAccessClass/tenant-root`, and
`BucketAccessClass/tenant-root-readonly` objects plus the SeaweedFS COSI
provisioner. The OpenBao app then renders its own nested system HelmRelease,
`openbao-guardian-system`.
The Harbor app follows the same pattern and renders its nested system
HelmRelease as `harbor-guardian-system`. It also renders standard COSI
`BucketClaim/harbor-guardian-registry` and
`BucketAccess/harbor-guardian-registry` objects, with bucket credentials written
to `Secret/harbor-guardian-registry-bucket`; the nested system chart then
translates the COSI `BucketInfo` into
`Secret/harbor-guardian-registry-s3` for Harbor's S3 registry storage. The live
gate checks those resources directly and waits for
`BucketClaim.status.bucketReady=true` plus
`BucketAccess.status.accessGranted=true` instead of relying only on the
top-level `Harbor/guardian` condition.

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
- `Ingress/ingress` in `tenant-root`: three ingress-nginx replicas for the root
  tenant ingress class that child tenants inherit.
- `SeaweedFS/seaweedfs` in `tenant-root`: root object storage at
  `s3.guardianintelligence.org`, three master, filer, volume, S3, and database
  replicas, three-way object replication, `10Gi` database PVCs, `20Gi` volume
  PVCs, and replicated storage for the
  database and volume PVCs. This app owns the tenant-root COSI bucket classes;
  do not check in hand-written `BucketClass` or `BucketAccessClass` objects.
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

Do not add a synthetic Harbor `BackupClass`. Cozystack 1.4's managed-app
BackupJob/RestoreJob flow is the standard path for database-style apps such as
Postgres and ClickHouse. Harbor's checked-in recovery proof is separate: the
Harbor app, nested system HelmRelease, registry COSI BucketClaim/BucketAccess,
and ORAS push/pull path must all work from the live cluster.

The checked-in External Secrets layer declares the backup Secrets for
`Postgres/guardian` and `ClickHouse/guardian` in `tenant-root`, `tenant-guardiancommercial-platform-dev`,
`tenant-guardiancommercial-platform-gamma`, and `tenant-guardiancommercial-platform-prod`:

| Namespace | Component | OpenBao role | OpenBao kv-v2 path |
| - | - | - | - |
| `tenant-root` | Postgres | `tenant-root-cnpg-backup` | `guardian/guardian-mgmt/tenant-root/postgres/guardian/cnpg-backup` |
| `tenant-guardiancommercial-platform-dev` | Postgres | `tenant-guardiancommercial-platform-dev-cnpg-backup` | `guardian/guardian-mgmt/tenant-guardiancommercial-platform-dev/postgres/guardian/cnpg-backup` |
| `tenant-guardiancommercial-platform-gamma` | Postgres | `tenant-guardiancommercial-platform-gamma-cnpg-backup` | `guardian/guardian-mgmt/tenant-guardiancommercial-platform-gamma/postgres/guardian/cnpg-backup` |
| `tenant-guardiancommercial-platform-prod` | Postgres | `tenant-guardiancommercial-platform-prod-cnpg-backup` | `guardian/guardian-mgmt/tenant-guardiancommercial-platform-prod/postgres/guardian/cnpg-backup` |
| `tenant-root` | ClickHouse | `tenant-root-clickhouse-backup` | `guardian/guardian-mgmt/tenant-root/clickhouse/guardian/backup` |
| `tenant-guardiancommercial-platform-dev` | ClickHouse | `tenant-guardiancommercial-platform-dev-clickhouse-backup` | `guardian/guardian-mgmt/tenant-guardiancommercial-platform-dev/clickhouse/guardian/backup` |
| `tenant-guardiancommercial-platform-gamma` | ClickHouse | `tenant-guardiancommercial-platform-gamma-clickhouse-backup` | `guardian/guardian-mgmt/tenant-guardiancommercial-platform-gamma/clickhouse/guardian/backup` |
| `tenant-guardiancommercial-platform-prod` | ClickHouse | `tenant-guardiancommercial-platform-prod-clickhouse-backup` | `guardian/guardian-mgmt/tenant-guardiancommercial-platform-prod/clickhouse/guardian/backup` |

Each Postgres path must contain `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`; ESO writes them to `guardian-cnpg-backup-creds` in the
tenant namespace. Each ClickHouse path must contain `bucketName`, `endpoint`,
`region`, `accessKey`, and `secretKey`; ESO writes them to
`guardian-clickhouse-backup-creds` in the tenant namespace. Populate these paths
from scoped provider-side backup credentials rather than preserving Kubernetes
Secrets as recovery state. The SecretStores talk to Cozystack's OpenBao service at
`http://openbao-guardian.tenant-root.svc:8200`, use the `kv` engine with
`version: v2`, and authenticate through the `kubernetes` auth mount with
audience `openbao`.
The ESO service accounts used for those OpenBao logins are also bound to the
standard Kubernetes `system:auth-delegator` ClusterRole so OpenBao's Kubernetes
auth method can validate their projected service-account tokens through the
TokenReview API.
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
applied with `aspect infra openbao-apply`, and real kv secret values to be
written with `aspect infra openbao-backup-secrets` before the sidecars and
scheduled jobs can succeed.

The checked-in environment app layer declares the same core service set in each
environment namespace:

- `tenant-guardiancommercial-platform-dev`: `Postgres/guardian`, `Harbor/guardian` at `harbor.dev.gi.org`,
  and `ClickHouse/guardian` with a daily backup Plan at `23 1 * * *`.
- `tenant-guardiancommercial-platform-gamma`: `Postgres/guardian`, `Harbor/guardian` at
  `harbor.gamma.gi.org`, and `ClickHouse/guardian` with a daily backup Plan at
  `29 1 * * *`.
- `tenant-guardiancommercial-platform-prod`: `Postgres/guardian`, `Harbor/guardian` at
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
before starting the load test. For built-in surfaces, the helper also prints
the backing Kubernetes app or Deployment YAML and waits for the relevant
Kubernetes/Cozystack readiness condition before k6 starts. The task then runs
the repo k6 script at `src/infrastructure/load/http-smoke.js` and prints k6's
standard CLI summary. This is the report input; do not wrap it in a
Guardian-specific evidence format.

When public DNS has not converged yet, pass `--host-overrides host=ip` to use
k6's native `hosts` option for that run. This is only a diagnostic split
between DNS and the cluster ingress path; the report must state the override,
and final public-DNS load evidence should omit it.

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

Company-site ingress diagnostic while DNS is stale:

```sh
aspect infra load-http \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --surface company-site \
  --stage gamma \
  --host-overrides gamma.gi.org=45.250.254.119 \
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
Before logging in, the helper prints the target `Harbor/guardian` app YAML and
the registry COSI resource YAML, then waits for the app `Ready` condition,
`BucketClaim.status.bucketReady=true`, `BucketAccess.status.accessGranted=true`,
and `WorkloadsReady`.

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
The helper does not expose ORAS `--insecure` or `--plain-http`; fix Harbor
certificate trust, DNS, or ingress before treating a registry load drill as
valid.

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
cluster, pass `--require-live=false --surface custom --url <url>`. Custom URLs
skip Kubernetes surface readiness because there is no canonical in-cluster
resource to prove. Production load reports must keep `--require-live=true`, use
a built-in surface where possible, and include the merged `--revision`.

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
Before applying the load Job, the helper prints the target app YAML and waits
for that Postgres or ClickHouse app's `Ready` and `WorkloadsReady` conditions,
so the report shows the benchmark ran against a reconciled, ready app.

The Postgres drill runs `pgbench` from the digest-pinned
`ghcr.io/cloudnative-pg/postgresql@sha256:6f64c83d80def98ab5b61bf36b1bbecea01dede382eef781dd9d1638b0d840c8`
image, connects to
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
`clickhouse/clickhouse-server@sha256:cc8c5bf275148b2de01a31e8fd6b55ba1ba2b0d3d08c23fafcb25b06e3c5dec5`
image, connects to
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
as `aspect infra live`. If `--revision` is provided, it also verifies that the
live Flux source and Kustomizations have applied that merged commit before
creating any backup or restore objects. It then creates a one-shot `BackupJob`,
waits for `BackupJob.status.phase=Succeeded`, waits for the resulting
`Backup.status.phase=Ready`, and prints standard Kubernetes resource YAML,
related Jobs/Pods, and pod logs where the backup strategy labels them.
If `--name` is omitted, the helper generates a unique UTC timestamped
`BackupJob` name. When `--restore-target` is set, the helper also validates the
generated `RestoreJob` name before it creates the `BackupJob`.
Before creating the `BackupJob`, the helper prints the source app YAML and
waits for that app's `Ready` and `WorkloadsReady` conditions.

To-copy restore drills need an empty same-kind target app in the same namespace.
Pass `--create-restore-target=true` with `--restore-target` to have the helper
create that target from the live source app's standard Cozystack CR spec before
the backup starts. The helper waits for the target to become ready, runs the
restore, rechecks and prints the restored target after `RestoreJob` succeeds,
and deletes a target it created by default. Pass
`--cleanup-created-restore-target=false` only when intentionally preserving the
drill target for manual inspection.

Run a ClickHouse backup smoke drill with:

```sh
aspect infra backup-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --stage dev \
  --component clickhouse
```

Run a restore drill into a temporary restore target app, not the serving app:

```sh
aspect infra backup-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --stage dev \
  --component clickhouse \
  --restore-target guardian-restore \
  --create-restore-target=true
```

The helper refuses in-place restore by default. `--allow-in-place-restore=true`
exists for an intentional repair operation, not for routine drills.

The same command supports `--component postgres`, but Postgres backup drills
will not pass until the corresponding `Postgres/guardian` app has
`spec.backup.enabled`, `destinationPath`, and `endpointURL` wired to declared
non-secret R2 coordinates. The reusable CNPG `BackupClass` and OpenBao-projected
credential Secrets already exist.

The backup drill intentionally does not support `--component harbor`. Harbor is
not a Cozystack managed-database BackupJob target in the 1.4 backup flow. Use
`aspect infra load-harbor-registry` for Harbor registry-path DR smoke evidence:
it waits for the Harbor app, COSI registry bucket readiness and access grant,
then exercises ORAS push/pull through the declared Harbor endpoint.

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
is non-empty with `sha256sum`, and removes the pod-local snapshot. Custom
`--snapshot-name` values must be simple ASCII filenames using only letters,
digits, dot, underscore, or hyphen. The report input is the native `bao`,
`kubectl`, and `sha256sum` output.

## Single Node Outage Drills

Use the Kubernetes eviction path first. `kubectl drain` is the standard
maintenance primitive here because it respects `PodDisruptionBudget` objects and
fails instead of bypassing an unsafe topology.

`aspect infra node-outage-drill` is a thin wrapper around repo-pinned `kubectl`
and a small repo-built helper. It first runs the same guardian-mgmt kubeconfig
guard as `aspect infra live`. If `--revision` is provided, it also verifies
source-controller convergence before cordoning the node. It then prints node,
pod, PDB, app, and dashboard status, proves the target node is currently
`Ready`, cordons and drains the selected node, prints the same status while the
node is drained, verifies the node is still cordoned, and proves the Guardian
surfaces are healthy before the node is uncordoned. Only after that outage-phase
gate passes does it uncordon the node and run the recovered-phase gate. Both
service-readiness gates require the dashboard deployments to be `Available`,
OpenBao to be ready with three statefulset replicas, root, dev, gamma, and prod
Postgres, Harbor, and ClickHouse apps to report `Ready` and `WorkloadsReady`,
each Harbor registry bucket to report
`BucketClaim.status.bucketReady=true` and
`BucketAccess.status.accessGranted=true`, and the dev/gamma/prod company-site
deployments to be `Available`. The recovered-phase gate also requires the
target node to be `Ready` after uncordon.

Run it against one node at a time:

```sh
aspect infra node-outage-drill \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)" \
  --node ash-earth \
  --confirm-node ash-earth
```

`--confirm-node` must exactly match `--node`; this is a deliberate guard because
the task mutates scheduling state. The helper does not pass `--force` or
`--disable-eviction` to `kubectl drain`, so PDB failures, unmanaged pods, or
other eviction problems stop the drill. If the drain or recovery checks fail
after cordon, the helper best-effort uncordons the node before exiting.

Capture the unmodified command output for PR-local evidence. Durable outage
reports should summarize the standard `kubectl` output from the preflight,
drained, outage-verified, and recovered phases, plus the relevant PDB, pod
placement, Cozystack app conditions, dashboard deployment conditions, and
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

The OG endpoints currently serve generated SVG cards. If PNG social-card
compatibility becomes required, add it as a pre-rendered build artifact instead
of reintroducing a native rasterizer in the request path.

Build the image with:

```sh
aspect build //src/products/company/site:image
```

Publish the image to the root Harbor registry after Harbor is reconciled:

```sh
aspect infra publish-company-site \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "$(git rev-parse HEAD)"
```

The publish task runs the same guardian-mgmt kubeconfig guard as
`aspect infra live`, verifies source-controller convergence when `--revision`
is provided, prints the root `Harbor/guardian` and registry COSI resources,
waits for the Harbor app `Ready` and `WorkloadsReady` conditions plus
`BucketClaim.status.bucketReady=true` and
`BucketAccess.status.accessGranted=true`, reads the root Harbor admin password
from `Secret/harbor-guardian-credentials`, opens a local `kubectl
port-forward` to `Service/harbor-guardian`, ensures the `guardian` Harbor
project exists and is public for unauthenticated cluster pulls, writes a
temporary Docker config for the local forwarded registry endpoint, and then
runs the existing Bazel `oci_push` target with a local repository override.
Do not run the raw `//src/products/company/site:push-harbor` target with
ambient workstation registry credentials for cluster publication; pushing
through the public ingress path can fail on large layer uploads and bypasses
the project-public invariant the deployments rely on.

The checked-in environment layer declares:

- `tenant-guardiancommercial-platform-dev`: `Deployment`, `Service`, `PodDisruptionBudget`, and `Ingress`
  for `dev.gi.org`.
- `tenant-guardiancommercial-platform-gamma`: `Deployment`, `Service`, `PodDisruptionBudget`, and
  `Ingress` for `gamma.gi.org`.
- `tenant-guardiancommercial-platform-prod`: `Deployment`, `Service`, `PodDisruptionBudget`, and
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
kubectl -n tenant-guardiancommercial get tenants.apps.cozystack.io
kubectl -n tenant-guardiancommercial-platform get tenants.apps.cozystack.io
kubectl -n tenant-root get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl get ns tenant-guardiancommercial-platform-dev tenant-guardiancommercial-platform-gamma tenant-guardiancommercial-platform-prod \
  -o custom-columns=NAME:.metadata.name,HOST:.metadata.labels.namespace\\.cozystack\\.io/host,INGRESS:.metadata.labels.namespace\\.cozystack\\.io/ingress
kubectl -n tenant-guardiancommercial-platform-dev get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl -n tenant-guardiancommercial-platform-gamma get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl -n tenant-guardiancommercial-platform-prod get postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io
kubectl -n cozy-dashboard get deployment/cozy-dashboard-console deployment/incloud-web-gatekeeper
kubectl -n cozy-dashboard get service/cozy-dashboard-console service/incloud-web-gatekeeper ingress/dashboard-web-ingress
kubectl -n tenant-root wait --for=condition=Ready tenants.apps.cozystack.io/guardiancommercial
kubectl -n tenant-guardiancommercial wait --for=condition=Ready tenants.apps.cozystack.io/platform
kubectl -n tenant-guardiancommercial-platform wait --for=condition=Ready tenants.apps.cozystack.io/dev tenants.apps.cozystack.io/gamma tenants.apps.cozystack.io/prod
kubectl -n tenant-root wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian openbaos.apps.cozystack.io/guardian
kubectl -n tenant-root wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-guardiancommercial-platform-dev wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-guardiancommercial-platform-dev wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-guardiancommercial-platform-gamma wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-guardiancommercial-platform-gamma wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-guardiancommercial-platform-prod wait --for=condition=Ready postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-guardiancommercial-platform-prod wait --for=condition=WorkloadsReady postgreses.apps.cozystack.io/guardian harbors.apps.cozystack.io/guardian clickhouses.apps.cozystack.io/guardian
kubectl -n tenant-guardiancommercial-platform-dev get deploy,svc,poddisruptionbudget,ingress company-site
kubectl -n tenant-guardiancommercial-platform-dev get networkpolicy company-site-ingress
kubectl -n tenant-guardiancommercial-platform-gamma get deploy,svc,poddisruptionbudget,ingress company-site
kubectl -n tenant-guardiancommercial-platform-gamma get networkpolicy company-site-ingress
kubectl -n tenant-guardiancommercial-platform-prod get deploy,svc,poddisruptionbudget,ingress company-site
kubectl -n tenant-guardiancommercial-platform-prod get networkpolicy company-site-ingress
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
kubectl -n tenant-guardiancommercial-platform-dev get secretstores.external-secrets.io openbao
kubectl -n tenant-guardiancommercial-platform-dev get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-guardiancommercial-platform-dev get secret guardian-cnpg-backup-creds
kubectl -n tenant-guardiancommercial-platform-dev get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-guardiancommercial-platform-dev get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-dev get secret guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-dev get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-guardiancommercial-platform-gamma get secretstores.external-secrets.io openbao
kubectl -n tenant-guardiancommercial-platform-gamma get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-guardiancommercial-platform-gamma get secret guardian-cnpg-backup-creds
kubectl -n tenant-guardiancommercial-platform-gamma get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-guardiancommercial-platform-gamma get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-gamma get secret guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-gamma get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl -n tenant-guardiancommercial-platform-prod get secretstores.external-secrets.io openbao
kubectl -n tenant-guardiancommercial-platform-prod get externalsecrets.external-secrets.io guardian-cnpg-backup-creds
kubectl -n tenant-guardiancommercial-platform-prod get secret guardian-cnpg-backup-creds
kubectl -n tenant-guardiancommercial-platform-prod get secretstores.external-secrets.io openbao-clickhouse-backup
kubectl -n tenant-guardiancommercial-platform-prod get externalsecrets.external-secrets.io guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-prod get secret guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-prod get plans.backups.cozystack.io guardian-clickhouse-daily
kubectl get cnpgs.strategy.backups.cozystack.io guardian-postgres-r2
kubectl get backupclasses.backups.cozystack.io guardian-postgres-cnpg
kubectl get altinities.strategy.backups.cozystack.io guardian-clickhouse-altinity
kubectl get backupclasses.backups.cozystack.io guardian-clickhouse-altinity
kubectl -n tenant-root wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-root wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-dev wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-guardiancommercial-platform-dev wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-gamma wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-guardiancommercial-platform-gamma wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
kubectl -n tenant-guardiancommercial-platform-prod wait --for=condition=Ready secretstores.external-secrets.io/openbao secretstores.external-secrets.io/openbao-clickhouse-backup
kubectl -n tenant-guardiancommercial-platform-prod wait --for=condition=Ready externalsecrets.external-secrets.io/guardian-cnpg-backup-creds externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds
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

Run the Harbor, Postgres, and ClickHouse child-resource checks in `tenant-guardiancommercial-platform-dev`,
`tenant-guardiancommercial-platform-gamma`, and `tenant-guardiancommercial-platform-prod` as well. `aspect infra live` performs those
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
- Flux `guardian-mgmt-platform` reconciles `src/infrastructure/base/cozystack`,
  `guardian-mgmt-storage` reconciles `src/infrastructure/base/storage`,
  `guardian-mgmt-base` reconciles `src/infrastructure/base`, and
  the product tenant graph reconciles `src/infrastructure/tenants/*` plus
  `src/infrastructure/products/platform/*`
- `Ingress/ingress`, `HelmRelease/ingress`, and nested
  `HelmRelease/ingress-nginx-system` are ready in `tenant-root`
- storage classes include `local`, `local-retain`, `replicated`, and
  `replicated-retain`; `replicated` is the only default class and has LINSTOR
  `autoPlace=3`; Piraeus has a LINSTOR `data` pool on `ash-earth`, `ash-wind`,
  and `ash-water`, sourced from stable NVMe by-id paths rather than volatile
  `/dev/nvme*` names
- root app resources exist for `SeaweedFS/seaweedfs`, `Postgres/guardian`,
  `Harbor/guardian`, and `ClickHouse/guardian` in `tenant-root`; SeaweedFS
  publishes root object storage at `s3.guardianintelligence.org`, creates the
  tenant-root COSI classes, and runs three master, filer, volume, S3, and
  database replicas on replicated storage; Postgres is replicated three ways on
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
  at `harbor-guardian-registry`, that it writes credentials to
  `harbor-guardian-registry-bucket`, and that the COSI controller reports
  `status.bucketReady=true` and `status.accessGranted=true`.
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
- the root/dev/gamma/prod ESO backup service accounts are subjects of
  `ClusterRoleBinding/guardian-openbao-secret-projection-auth-delegator`,
  which points at `ClusterRole/system:auth-delegator`
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

`aspect infra validate` enforces that boundary before rendering or live checks:
it fails if operator scratch credentials (`DELETE_ME.env`), management-cluster
evidence directories, the retired `guardian-mgmt.json` inventory, or the old
custom release evidence schema path are reintroduced. Guardian management
topology belongs in the OpenTofu root, and proof belongs in standard tool output.

The same validation path prevents partial Postgres backup activation. A
`Postgres/guardian` app may omit `spec.backup` until the real R2 coordinates are
known; once it includes `spec.backup`, the app must carry concrete
`destinationPath` and `endpointURL` values and the same manifest set must include
`Plan/guardian-postgres-daily` targeting `BackupClass/guardian-postgres-cnpg`.
This keeps the reusable CNPG strategy present without silently deploying a
non-restorable Postgres backup surface.

## Not Done In This Substrate Slice

These are intentionally outside the merged L2/OpenTofu substrate and need
separate PRs with their own validation:

- Destructive first-node host-bootstrap for a freshly provisioned Latitude box:
  render/apply Talos configs in maintenance mode, bootstrap etcd exactly once,
  install Cozystack, apply the initial Flux handoff, and persist the encrypted
  genesis bundle under operator state.
- Latitude VLAN assignment imports, once assignment IDs are collected.
- Live publication of the checked-in TanStack company-site OCI image to Harbor
  with `aspect infra publish-company-site`, followed by live readiness evidence
  for dev, gamma, and prod. The task surface is declared and validated, but the
  live publish still requires current guardian-mgmt credentials and
  source-controller convergence on the merged revision.
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
  live backup/restore drills for Postgres and ClickHouse. The package
  prerequisites, backup controller deployments/RBAC/CRDs, reusable CNPG
  BackupClass, reusable ClickHouse Altinity BackupClass, Postgres / ClickHouse
  credential SecretStores and ExternalSecrets, OpenBao auth/policy
  configuration, ClickHouse app backup Secret references, recurring ClickHouse
  backup Plans, and Harbor's COSI-backed registry bucket resources are declared
  and live-gated. `aspect infra backup-drill` now creates ad-hoc Cozystack
  BackupJob/RestoreJob resources, can create and clean up a temporary to-copy
  restore target from the live source app spec, and prints standard Kubernetes
  evidence. Applying the OpenBao root, populating real kv values, Postgres
  object-store coordinates, running the smoke tests, Harbor registry-path
  validation through ORAS/COSI, and live restore drill reports still need
  current cluster credentials and source-controller convergence.
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
