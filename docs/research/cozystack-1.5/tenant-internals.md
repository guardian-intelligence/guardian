# Cozystack 1.5 — Tenant internals (what a Tenant CR actually renders)

Version-accurate for the v1.5.0 tag (commit `82b8c460`, 2026-06-22) — the exact version this
cluster pins (`images.lock`: `cozy-installer:1.5.0`). Paths are cozystack-repo-relative at that
ref. Where a claim was additionally verified against the live `guardian-mgmt` cluster it is
marked **[verified live]**.

## The Tenant "CRD" is not a CRD

`apps.cozystack.io/v1alpha1 Tenant` is an aggregated-API front over a Flux **HelmRelease** of
the chart `packages/apps/tenant`, declared by an ApplicationDefinition
(`packages/system/tenant-rd/cozyrds/tenant.yaml:30-37`, `release.prefix: tenant-`). There is no
tenant controller; Flux reconciles the chart. The RD carries
`release.cozystack.io/helm-install-timeout: "15m"` (line 18) because helm-controller's default
5m wait on a parent tenant fires before children reach Ready and starts a rollback oscillation.

The whole values surface (`packages/apps/tenant/values.yaml`, 27 lines): `host`, `etcd`,
`monitoring`, `ingress`, `gateway` (absent-by-default tri-state), `seaweedfs`,
`schedulingClass`, `resourceQuotas`.

## Naming and hierarchy

- Release name must be `tenant-<word>` with **exactly one dash**; tenant names cannot contain
  dashes at all (`_helpers.tpl:1-15` hard-fails; README.md:9-17 — `tenant-foo-bar` would parse
  ambiguously). Child namespace = `<parent-namespace>-<name>`: `tenant-root`→`tenant-guardian`,
  `tenant-guardian`→`tenant-guardian-beta`. Deep nesting accumulates toward the 63-char RFC 1123
  limit.
- The child Namespace carries an **ownerReference to the parent Namespace**
  (`templates/namespace.yaml:96-102`, `blockOwnerDeletion: true, controller: true`) — deleting a
  parent tenant namespace garbage-collects the entire subtree.
- Rendering does a live `lookup` of the parent Namespace and hard-fails if absent
  (`namespace.yaml:2-5`) — the v1.5.0 chart cannot be rendered offline (relaxed on main).

## Inheritance: one Secret is the whole mechanism

`namespace.yaml:104-126` writes Secret `cozystack-values` into the new namespace: `_cluster`
(copied from the parent) + computed `_namespace: {etcd, ingress, gateway, monitoring,
seaweedfs, host, schedulingClass}`. Every app HelmRelease in the tenant consumes it via
`valuesFrom`. Service resolution is **nearest ancestor, inclusive** (`namespace.yaml:22-64`):
each flag starts from the parent's already-resolved value and is overridden to self only if this
tenant enables the service. The resolved owner is also stamped on the namespace as
`namespace.cozystack.io/<svc>` labels. **[verified live]** — all guardian tenants resolve
`etcd/ingress/monitoring/seaweedfs: tenant-root`.

Host derivation (`namespace.yaml:13-19`): explicit `spec.host` wins, else
`<lastNameSegment>.<parent host>`. Apps read it as `.Values._namespace.host`
(`cozy-lib` `ns-host`). TLS: per-host ACME by default; an operator wildcard cert applies only to
the publishing namespace's nginx (`extra/ingress/templates/nginx-ingress.yaml:52-62`) — child
tenant ingress controllers cannot point at it (upstream issue #2820).

## RBAC ladder

`templates/tenant.yaml`: a ServiceAccount named like the namespace; RoleBinding `cozy:tenant`
whose subjects are the tenant-root SA + one SA per ancestor + self (this is "higher-level
tenants can view and manage the applications of all their children tenants", README.md:44);
four RoleBindings `cozy:tenant:{view,use,admin,super-admin}` binding **Groups named
`<tenant-namespace>-<level>`** for self and every ancestor (`cozy-lib/templates/_rbac.tpl:84-96`
— a parent's admin group is automatically admin in every descendant). ClusterRoles are
label-aggregated in `cozystack-basics/templates/clusterroles.yaml`; only **super-admin** can
create nested tenants (`apps.cozystack.io/*` verbs `*`, lines 265-281); plain admin's app list
(~27 kinds) explicitly excludes `tenants`, `monitoring`, `etcd`, `ingress` (lines 198-251).

The Keycloak side (`templates/keycloakgroups.yaml:3`) renders `KeycloakRealmGroup`s **only when
`_cluster.oidc-enabled == "true"` AND the EDP Keycloak-operator CRD exists** — silently skipped
otherwise. Consequence for this cluster: platform OIDC is disabled
(`src/infrastructure/base/cozystack/platform.yaml:39-44`), so the per-tenant group machinery is
inert here; the RBAC argument for tenants does not currently apply to us.

## Network model: isolation rides on SENDER EGRESS

All policies are Cilium CRDs (`templates/networkpolicy.yaml`, 259 lines; no vanilla
NetworkPolicy, no Kyverno — admission is VAPs). The counter-intuitive core:
`allow-external-communication` (lines 16-29) allows ingress `fromEntities: [world, cluster]` —
receivers are wide open; what isolates tenants is the sender-side default-deny plus:

- `<tenant>-egress` CCNP (lines 31-81): egress allowed to (a) namespaces labeled
  `tenant.cozystack.io/<tenant>` — self + all descendants; (b) ancestors' `vminsert`; (c)
  ancestors' etcd pods; (d) ancestors' pods labeled `cozystack.io/service: ingress`. Nothing
  else — **siblings are unreachable in either direction**, and a child cannot reach arbitrary
  parent pods.
- `<tenant>-ingress` CCNP (lines 83-111): from kube-apiserver, `cozystack.io/system=true`
  namespaces, kube-system, and ancestor namespaces.
- Opt-in pod labels `policy.cozystack.io/allow-to-apiserver: "true"` / `allow-to-etcd: "true"`
  (default: pods cannot reach the k8s API; note upstream #2773 — the apiserver hole opens 6443
  only, breaking in-cluster clients that assume :443).
- Hardcoded egress allows for every tenant: kube-dns, cozy-dashboard, cozy-linstor (whole
  namespace, deliberately, to cover transient ACME solver pods — comment at lines 190-194),
  keycloak, cdi-upload-proxy, and any pod labeled `cozystack.io/service: ingress` anywhere.

There is **no first-class sibling-to-sibling peering** (upstream proposal #2656 open). Cross-
sibling traffic requires hand-written CCNP pairs (egress on sender + ingress hole on receiver if
the receiver has its own tightening policy).

## Quotas

`templates/quota.yaml`: ResourceQuota `tenant-quota` **and** LimitRange `tenant-range-limits`
(container default 250m/128Mi–2Gi) render only if `resourceQuotas` is non-empty — a tenant with
the default `resourceQuotas: {}` has **neither** quota nor LimitRange. `cpu` maps to
requests+limits; count quotas (`services.loadbalancers`) pass through. `schedulingClass` landed
in the 1.2 line.

## Per-tenant optional services: the real cost multipliers

Each flag renders a HelmRelease into the tenant namespace (labeled
`internal.cozystack.io/tenantmodule: "true"`, `sharding.fluxcd.io/key: tenants`,
`remediation.retries: -1`):

- **monitoring** → full per-tenant observability stack (`packages/system/monitoring` via
  `extra/monitoring`): per metricsStorage (default two: shortterm 3d + longterm 14d, 10Gi) a
  VMCluster with vminsert×2/vmselect×2/vmstorage×2 at replicationFactor 2; a VLCluster
  (vlinsert/vlselect/vlstorage ×2); VMAgent+VMAlert; Grafana ×2 + CNPG ×2; Alerta + CNPG ×2 +
  VMAlertmanager ×3. Floor ≈ 20+ pods, ~10 PVCs **per tenant**. Most components ship
  `resources: {}` + VPA, so usage floats. Monitoring visibility boundary = the tenant that RUNS
  monitoring ("only that tenant will have access to them" — chart README), not the producer.
- **ingress** → dedicated ingress-nginx ×2, **ingressClass named after the namespace**
  (`extra/ingress/templates/nginx-ingress.yaml:47-68`), pods labeled
  `cozystack.io/service: ingress`, `Service type: LoadBalancer` from the shared MetalLB pool.
- **etcd** → aenix etcd-operator EtcdCluster ×3, 4Gi each — only needed to host tenant
  Kubernetes clusters.
- **seaweedfs** → CNPG ×2 + masters ×3 + filer ×2 + volume ×2 + s3 ×2.
- **gateway** → Cilium Gateway-API Gateway + own LB IP + Certificate. The chart's docs claim
  auto-enable for derived-apex tenants in three places, but `_helpers.tpl:56-62` returns false
  whenever `gateway` is unset — **explicit-opt-in only at v1.5.0**; the doc/code contradiction
  is unresolved in-tag.
- **info** (unconditional) → kubeconfig Secret for the tenant, no workloads.

A bare tenant (all flags false) costs: one HelmRelease, a namespace, ~10 Cilium policies, RBAC
objects, the `cozystack-values` Secret. Near-free. **[verified live]** — all five guardian
tenants run all-flags-false and inherit tenant-root services.

## Shared regardless of tenancy

StorageClasses (LINSTOR CSI is cluster-wide; quota `storage` is the only tenant-level storage
control), MetalLB IPAddressPools (only cap: `services.loadbalancers` quota), the root ingress
(platform `publishing.ingressName` default `tenant-root`; the shared nginx watches ALL
namespaces for its class), kube-dns, dashboard, the Flux shard reconciling all tenant HRs, and
host-level monitoring agents which ship platform metrics into **tenant-root's** monitoring
(`bundles/system.yaml:207`) — tenant-root's Monitoring app is effectively mandatory.

## Tenant deletion

Helm `pre-delete` hook Job **in cozy-system** (`templates/cleanup-job.yaml`) deletes the
tenant's HelmReleases in two waves (user apps, then tenant modules). The Job image at v1.5.0 is
**`bitnami/kubectl:latest`** (line 65) — unpinned, and Bitnami's public catalog was discontinued
in 2025. Main replaces it with `clastix/kubectl:v1.32` + non-root (commits `5e7936c8`,
`e1f6a9c2`, `ed30425b`; in **no** 1.5.x release). Consequences here: tenant deletion pulls an
unpinned image that is not in our `images.lock` (dark/airgapped mode: deletion hook cannot
pull → hangs), and upstream has multiple deletion wedge modes on 1.5.x (#3018 namespace stuck
Terminating, #2961 etcd-operator hangs on tenant re-creation, #2473 HR stuck when tenant
controlplane is down). Treat tenant delete/recreate as a known-fragile operation on 1.5.0;
prefer static, long-lived tenants.
