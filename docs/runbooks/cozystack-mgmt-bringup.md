# Cozystack Management Cluster Bring-Up

This runbook describes the current `guardian-mgmt` substrate: a three-node
Talos/Cozystack management cluster on one Latitude.sh Virtual Network with real
L2/ARP. The source files are the authority; this document explains their shape
and the standard checks to run.

## Source Of Truth

| Layer | File |
| - | - |
| Bare-metal and VLAN state | `src/infrastructure/bootstrap/guardian-mgmt/*.tf` |
| OpenBao API configuration | `src/infrastructure/bootstrap/guardian-mgmt-openbao/*.tf` |
| Public DNS records | `src/infrastructure/bootstrap/guardian-mgmt-dns/*.tf` |
| Talos/Talm chart | `src/infrastructure/talm/` |
| Cozystack platform package | `src/infrastructure/base/cozystack/platform.yaml` |
| Core Cozystack apps | `src/infrastructure/base/apps/core-services.yaml` |
| Observability apps | `src/infrastructure/base/apps/observability.yaml`, `src/infrastructure/products/platform/*/observability.yaml` |
| MetalLB L2 pool | `src/infrastructure/base/networking/metallb.yaml` |
| Kube-OVN MTU | `src/infrastructure/base/networking/subnet-mtu.yaml` |
| Flux handoff | `src/infrastructure/base/flux/sync.yaml` |
| OpenBao app | `src/infrastructure/base/openbao/` |
| LINSTOR storage | `src/infrastructure/base/storage/` |
| Product tenant topology | `src/infrastructure/tenants/guardian-commercial/`, `src/infrastructure/tenants/platform/` |
| Product stage apps | `src/infrastructure/products/platform/` |
| Company-site OCI artifact | `src/products/company/site/` |

`src/infrastructure/bootstrap/guardian-mgmt/main.tf` is the non-secret topology
record for the Latitude project/site, the management VLAN, and the three adopted
control-plane servers. Use its standard OpenTofu outputs as the
machine-readable interface for scripts, Aspect tasks, and future bootstrap CLI
code. Do not add a parallel inventory file for the same node/IP/VLAN facts.

## Current Facts

`guardian-mgmt` uses Latitude project `proj_R82A0yqmd06mM` in ASH and Virtual
Network `vlan_8mop5gkpP5jxv`, VID `2140`.

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
- Public ingress uses the three public node IPs in the Cozystack platform package.
- Kube-OVN subnet MTU is `1362`: `1420` VLAN path MTU minus `58` bytes of GENEVE.
- The default StorageClass is `replicated`: three-way LINSTOR/DRBD on the
  checked-in `data` pool.

## Pinned Platform

The checked-in Talm chart sets:

- Talos image `ghcr.io/cozystack/cozystack/talos:v1.13.0`
- `templateOptions.talosVersion: "v1.13"`
- Kubernetes `v1.34.3`
- endpoint `https://10.8.0.250:6443`
- floating IP `10.8.0.250`
- VIP link `enp1s0f0.2140`
- advertised subnets `["10.8.0.0/24"]`
- cert SANs for the VIP and each public node IP

Generated Talm secrets, kubeconfig, and local operator state must stay out of
Git. The per-node Talm patches are checked in because they contain non-secret
physical facts required for unattended rebuilds: stable install disk serials,
public endpoints, and the VLAN address overlay. Do not replace install disk
selectors with `/dev/nvme*`; NVMe names can swap across boots.

## Bootstrap Path

Run from the repo root:

```sh
aspect infra tofu-init

aspect infra bootstrap \
  --revision "<merged-main-commit-sha>"

aspect infra openbao-drill \
  --mode init-unseal \
  --revision "<merged-main-commit-sha>"

aspect infra openbao-apply \
  --revision "<merged-main-commit-sha>"
```

`aspect infra bootstrap` initializes the standard OpenTofu S3 backend, prints
the standard OpenTofu topology outputs, runs `aspect infra validate`, refreshes
the gitignored Talm kubeconfig, runs the Talos L2 gate, upgrades the Cozystack
installer/operator to the repo-pinned version, and then runs the live
source-controller checks on the requested merged `main` revision.
`aspect infra upgrade-cozystack` is the narrow day-two path for existing
clusters when only the Cozystack installer/operator release needs to move.

OpenBao API configuration is a separate post-app step. Initialize/unseal the
Cozystack OpenBao app with `aspect infra openbao-drill --mode init-unseal`, then
run `aspect infra openbao-apply`. The apply task waits for the OpenBao
StatefulSet, reads the cluster-local bootstrap token Secret without printing
secret material, opens a local `kubectl port-forward`, initializes the standard
R2-backed OpenTofu backend, and applies
`src/infrastructure/bootstrap/guardian-mgmt-openbao`.

## Flux Handoff

After Talos is bootstrapped and Cozystack is installed, Flux owns the
post-Kubernetes desired state:

- `guardian-mgmt-platform` reconciles `src/infrastructure/base/cozystack`.
- `guardian-mgmt-storage` reconciles `src/infrastructure/base/storage`.
- `guardian-mgmt-base` reconciles `src/infrastructure/base`.
- `guardian-mgmt-tenant-apps` reconciles
  `src/infrastructure/tenants/guardian-commercial`.
- `guardian-mgmt-platform-tenant` reconciles
  `src/infrastructure/tenants/platform`.
- `guardian-mgmt-platform-<stage>-tenant` reconciles the dev, gamma, and prod
  stage tenants.
- `guardian-mgmt-platform-dev`, `guardian-mgmt-platform-gamma`, and
  `guardian-mgmt-platform-prod` reconcile product app state in dev -> gamma ->
  prod order.

After changing checked-in infrastructure, merge the PR to `main` and let the
existing `GitRepository/guardian` and `Kustomization/guardian-mgmt-*` objects
pull the new revision. Do not manually apply the rendered base as a substitute
for source-controller validation; manual apply is only the bootstrap handoff
before Flux owns the path.

The `guardian-mgmt-platform` and `guardian-mgmt-storage` slices stay
apply-only because they own the Cozystack platform package and storage
substrate. `guardian-mgmt-base` and the tenant/product slices prune their owned
inventory so renamed or removed app objects do not remain live after a merge.

## Cozystack Apps

The platform package uses Cozystack's `isp-full` variant. Cozystack provides the
backup controller, backupstrategy controller, Velero wiring, and the
platform-managed `BackupClass/cozy-default` with system bucket `cozy-backups`.
Guardian does not check in custom backup strategies or per-app backup credential
Secrets.

The checked-in root app slice declares:

- `Postgres/guardian` in `tenant-root`: CNPG-backed, three replicas, explicit
  `storageClass: replicated`, synchronous commit quorum `1..2`, and a daily
  backup Plan at `7 1 * * *`.
- `Ingress/ingress` in `tenant-root`: three ingress-nginx replicas for the root
  tenant ingress class that child tenants inherit.
- `SeaweedFS/seaweedfs` in `tenant-root`: root object storage at
  `s3.guardianintelligence.org`, three-way object replication, and replicated
  storage for database and volume PVCs.
- `Harbor/guardian` in `tenant-root`: `harbor.guardianintelligence.org`, with
  replicated storage for the registry database, Redis, Trivy, and chart-owned
  PVCs.
- `ClickHouse/guardian` in `tenant-root`: three ClickHouse replicas plus three
  Keeper replicas, with a daily backup Plan at `17 1 * * *`.
- `Monitoring/monitoring` in `tenant-root`: Cozystack's Grafana,
  VictoriaMetrics, VictoriaLogs, Alerta, and VMAgent stack at
  `grafana.guardianintelligence.org`, with replicated metrics and logs storage.

The platform dev/gamma/prod product stage namespaces declare the same Postgres,
Harbor, ClickHouse, and Monitoring service set with smaller storage sizes,
stage-specific backup schedules, and Grafana hosts at `grafana.<stage>.gi.org`.
Those Grafana hostnames are part of the OpenTofu-managed public DNS desired
state in `src/infrastructure/bootstrap/guardian-mgmt-dns`.

## Backups

Postgres and ClickHouse backups use the Cozystack 1.5 system bucket flow:

```yaml
backup:
  enabled: true
  schedule: ""
  useSystemBucket: true
```

Postgres also carries `retentionPolicy: 30d`. Recurring backup `Plan` resources
target `backupClassName: cozy-default`.

Required platform resources:

```sh
kubectl get backupclasses.backups.cozystack.io cozy-default
kubectl get cnpgs.strategy.backups.cozystack.io cozy-default-cnpg
kubectl get altinities.strategy.backups.cozystack.io cozy-default-altinity
kubectl -n tenant-root get bucket cozy-backups
kubectl -n tenant-root get secret bucket-cozy-backups-system-credentials
kubectl -n cozy-velero get backupstoragelocations.velero.io cozy-default
```

On existing Postgres releases, run an immediate `BackupJob` after enabling
`useSystemBucket` so CNPG WAL archiving is activated through the Cozystack
backup flow.

`aspect infra backup-drill` is the standard ad-hoc backup/restore operator
surface. It creates Cozystack `BackupJob` and `RestoreJob` resources against
`BackupClass/cozy-default`, waits on native status, and prints standard
Kubernetes resource YAML, Jobs/Pods, and logs. Do not check one-shot
`BackupJob` resources into the Flux path; they are operation records, not
steady desired state.

## Live Verification

Use repo-pinned tools through Aspect:

```sh
aspect infra validate

aspect infra talos

aspect infra upgrade-cozystack \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG"

aspect infra live \
  --kubeconfig "$GUARDIAN_MGMT_KUBECONFIG" \
  --revision "<merged-main-commit-sha>"
```

The live gate checks Flux reconciliation, Cozystack packages, backup platform
resources, MetalLB/L2, Kube-OVN MTU, LINSTOR storage classes, OpenBao, root and
product-stage app CRs, child HelmReleases and workload resources, Harbor COSI
registry bucket access, Postgres/CNPG child resources, ClickHouse/Altinity child
resources, Monitoring app readiness and storage settings, and company-site
deployment shape. It also checks known stale resources are absent after Flux
prunes the owning inventory.

Kubernetes-side readiness evidence should be captured as PR-local command output
while a change is being reviewed. Durable operational proof should come from
standard tools already in the stack: Flux status, Kubernetes conditions,
Cozystack backup/restore resources, load-test tool output, and monitoring data.
Do not add repo-specific JSON evidence bundles or durable CLI/task surfaces whose
only purpose is temporary PR verification.

## Load And DR Drills

Use the existing drill/load tasks after source-controller converges the target
revision:

```sh
aspect infra load-db --stage dev --component postgres
aspect infra load-db --stage dev --component clickhouse
aspect infra load-harbor-registry --stage dev
aspect infra backup-drill --stage dev --component postgres
aspect infra backup-drill --stage dev --component clickhouse
aspect infra node-outage-drill --node ash-earth
```

Repeat the same checks through gamma and prod after promotion. Reports should be
checked in only for final DR/load results, not for temporary PR evidence.
