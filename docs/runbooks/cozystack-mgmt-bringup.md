# Cozystack Management Cluster Bring-Up

This runbook describes the current `guardian-mgmt` substrate checked into this
repo: a three-node Talos/Cozystack management cluster on one Latitude.sh Virtual
Network with real L2/ARP. It replaces the old public-/31 KubeSpan procedure.

The source files are the authority. This document explains their shape and the
checks to run; it is not a separate source of truth.

## Source Of Truth

| Layer | File |
| - | - |
| Latitude inventory | `src/infrastructure/inventory/guardian-mgmt.json` |
| Bare-metal state | `src/infrastructure/bootstrap/guardian-mgmt/*.tf` |
| Talos/Talm chart | `src/infrastructure/talm/` |
| Cozystack platform package | `src/infrastructure/base/cozystack/platform.yaml` |
| Core Cozystack apps | `src/infrastructure/base/apps/core-services.yaml` |
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

## Validate OpenTofu

The repo pins OpenTofu in `MODULE.bazel` and the Latitude provider in
`src/infrastructure/bootstrap/guardian-mgmt/.terraform.lock.hcl`.

Run the full local substrate check with:

```sh
aspect infra validate
```

Run the active manifest invariant test with:

```sh
bazelisk test //src/infrastructure/tests:manifest_invariants_test
```

That test parses the checked-in Kubernetes YAML and verifies the platform
package publishes the dashboard/API endpoints, environment tenants use the
expected `*.gi.org` hosts, MetalLB and Kube-OVN keep the L2/MTU topology,
`replicated` is the only default StorageClass, root and environment
Postgres/Harbor/ClickHouse apps use the intended HA/storage shape, OpenBao stays
declared in `tenant-root`, the company site is declared for dev/gamma/prod, and
Flux reconciles base before tenant apps.

Collect live readiness evidence with:

```sh
aspect infra evidence \
  --output-dir docs/reports/infrastructure/live/management-readiness \
  --kube-context guardian-mgmt
```

This task builds the repo-pinned `kubectl`, queries the live management cluster,
and writes raw JSON snapshots plus `management-readiness.json` and
`management-readiness.md` under the selected output directory. It exits nonzero
when the cluster is unreachable or when expected readiness checks fail; do not
check in failed output as proof of readiness.

Local validation does not require backend credentials:

```sh
bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" fmt -check

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" \
  init -backend=false -input=false -reconfigure

bazelisk run @opentofu_linux_amd64//:tofu_bin -- \
  -chdir="$PWD/src/infrastructure/bootstrap/guardian-mgmt" validate
```

Live planning requires:

- `LATITUDESH_AUTH_TOKEN` for the Latitude provider.
- S3-compatible backend credentials for R2 through the usual `AWS_*`
  environment variables.
- The R2 endpoint passed during backend initialization, because OpenTofu's S3
  backend cannot read it from `guardian-mgmt.json`.

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

The next bootstrap CLI slice should wrap this chart with repo-pinned `talm` and
`talosctl` artifacts, render each node, apply the first config in Talos
maintenance mode, bootstrap etcd exactly once, and persist the encrypted genesis
bundle under operator state.

## Kubernetes Handoff

After Talos is bootstrapped and Cozystack is installed, apply the Flux handoff
once:

```sh
kubectl apply -f src/infrastructure/base/flux/sync.yaml
```

Flux first reconciles `src/infrastructure/base`, including the Platform package,
root Postgres/Harbor/ClickHouse apps, networking manifests, storage classes,
environment tenants, OpenBao, and the Flux objects themselves. The base also
declares a second Flux Kustomization, `guardian-mgmt-tenant-apps`, that depends
on `guardian-mgmt-base` and reconciles `src/infrastructure/environments` after
the Tenant chart has had a chance to create `tenant-dev`, `tenant-gamma`, and
`tenant-prod`.

Both Flux Kustomizations are apply-only (`wait: false`). Cozystack app CRs fan
out into HelmReleases and stateful workloads; readiness is proven by the live
checks and checked-in load/DR/outage reports, not by treating Flux's apply
status as service health.

For a direct render check from the repo-pinned kubectl artifact:

```sh
bazelisk build @kubectl_linux_amd64//file:file
OUTPUT_BASE="$(bazelisk info output_base)"
"$OUTPUT_BASE/external/+http_file+kubectl_linux_amd64/file/kubectl" \
  kustomize src/infrastructure/base
```

## Cozystack App Path

Cozystack 1.4 serves `apps.cozystack.io/v1alpha1` resources through its
aggregated API server. The API server reads `ApplicationDefinition` objects at
startup, then converts app resources such as `Tenant`, `Postgres`, `Harbor`, and
`ClickHouse` into Flux `HelmRelease` objects.

For `Tenant`, the Cozystack source sets `release.prefix: tenant-`. Applying
`Tenant/dev` in `tenant-root` therefore creates a `HelmRelease` named
`tenant-dev` in `tenant-root`. The tenant chart then creates namespace
`tenant-dev` and writes that namespace's `cozystack-values` Secret. The checked
in `dev`, `gamma`, and `prod` tenants intentionally inherit root `etcd`,
ingress, monitoring, and SeaweedFS; they only set explicit environment hostnames
for now.

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
  DRBD until the upstream chart renders that field.

Backups are off in this first root app declaration. Enable them only by pointing
the app specs at pre-existing Kubernetes Secrets delivered from the declared
OpenBao/R2 secret path; never put S3 credentials directly in app specs.

The checked-in environment app layer declares the same core service set in each
environment namespace:

- `tenant-dev`: `Postgres/guardian`, `Harbor/guardian` at `harbor.dev.gi.org`,
  and `ClickHouse/guardian`.
- `tenant-gamma`: `Postgres/guardian`, `Harbor/guardian` at
  `harbor.gamma.gi.org`, and `ClickHouse/guardian`.
- `tenant-prod`: `Postgres/guardian`, `Harbor/guardian` at
  `harbor.prod.gi.org`, and `ClickHouse/guardian`.

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
kubectl -n tenant-dev get deploy,svc,ingress company-site
kubectl -n tenant-gamma get deploy,svc,ingress company-site
kubectl -n tenant-prod get deploy,svc,ingress company-site
kubectl -n tenant-root get openbao guardian
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
- each tenant namespace has the company-site `Deployment`, `Service`, and
  `Ingress`; the dev and gamma ingress hosts are `dev.gi.org` and
  `gamma.gi.org`, and prod is `guardianintelligence.org`
- OpenBao is deployed as the Cozystack-managed `guardian` app in `tenant-root`

Talos-side network checks:

```sh
talosctl --nodes 10.8.0.11,10.8.0.12,10.8.0.13 --endpoints 10.8.0.250 get addresses
talosctl --nodes 10.8.0.11,10.8.0.12,10.8.0.13 --endpoints 10.8.0.250 get routes
talosctl --nodes 10.8.0.11,10.8.0.12,10.8.0.13 --endpoints 10.8.0.250 get kubespanpeerstatuses
```

Expected result: addresses and routes show the VLAN subnet, and KubeSpan has no
active mesh peers.

The `aspect infra evidence` task automates the Kubernetes-side readiness checks
above. It intentionally does not replace the later load-test,
disaster-recovery, or single-node-outage reports; those reports still need live
drills and checked-in evidence for each component.

## Not Done In This Substrate Slice

These are intentionally outside the merged L2/OpenTofu substrate and need
separate PRs with their own validation:

- Bootstrap CLI wrapper for the full Talm/Talos path.
- Latitude VLAN assignment imports, once assignment IDs are collected.
- Publishing the checked-in company-site OCI image to Harbor and capturing live
  readiness evidence for dev, gamma, and prod.
- Dashboard readiness evidence beyond the Cozystack platform package exposure.
- Load-test reports for CNPG/Postgres, Harbor, ClickHouse, OpenBao, the
  Cozystack dashboard, and the company-site surfaces.
- Backup specs for root and environment Postgres/Harbor/ClickHouse, wired to
  declared OpenBao/R2-projected Secrets.
- ClickHouse chart-side `spec.storageClass` rendering, because Cozystack 1.4
  still relies on the cluster default for ClickHouse and keeper PVCs.
- OpenBao init/unseal automation and backup/restore drills.
- Checked-in load-test, disaster-recovery, and single-node-outage reports for
  each new infrastructure component.
