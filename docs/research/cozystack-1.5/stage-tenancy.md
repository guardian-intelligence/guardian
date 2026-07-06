# Cozystack 1.5 — tenants as deployment stages: intent, precedent, and live bugs

Companion to `tenant-internals.md`. Question under research: is modeling beta/gamma/prod as
Cozystack Tenants an intended pattern, what does it buy over plain namespaces in one tenant,
and what does the v1.5.0 line get wrong. Verified against the v1.5.0/v1.5.1/v1.5.2 tags and the
live `guardian-mgmt` cluster (pinned `cozy-installer:1.5.2` since #434).

## Intent: blessed, but not the maintainers' default mental model

- The v1.5 getting-started doc blesses it verbatim: "Tenants are the isolation mechanism in
  Cozystack. They are used to separate clients, teams, **or environments**"
  (`website: content/en/docs/v1.5/getting-started/create-tenant.md`).
- Every maintainer-authored *example*, however, maps tenant→team or tenant→customer and puts
  environments elsewhere: the concepts page says "a tenant usually belongs to a team or
  subteam" / "a customer", and assigns dev/test/prod to tenant **Kubernetes clusters** ("They
  are used as development, testing, and production environments"). The June 2026 official blog
  deploys a cluster named `kubernetes-dev` into `tenant-team1`. The official GitOps example
  (aenix-io/cozystack-gitops-example) has ONE tenant and a chart literally named `staging`
  released into it.
- In-the-wild corpus is tiny (~21 GitHub code hits for `kind: Tenant` total, most in
  cozystack's own repos). Closest analogues: vgijssel/setup (homelab, a dedicated `prod`
  tenant with ingress+monitoring+seaweedfs enabled) and kingdon-ci/tenants (Flux maintainer,
  `tenant-test` as sandbox, envs otherwise split by release name inside tenant-root). **No
  public repo models a 3-stage tenant split** — this repo's `stage-tenants.yaml` appears to be
  the largest public example of the pattern. Absence isn't damning (under ten public Cozystack
  GitOps repos exist), but there is no precedent to crib from; we are the reference.

## What a stage-tenant buys over a namespace-in-one-tenant

1. **Sibling default-deny for free**: sender-egress CCNPs mean beta pods cannot reach prod pods
   in either direction, with zero policy authoring (see `tenant-internals.md`, network model).
   Plain namespaces inside one tenant share `allow-internal-communication`-equivalent openness
   unless we hand-write per-namespace default-deny.
2. **Domain hierarchy**: `spec.host` per stage (`beta.guardianintelligence.org`) propagated to
   every app via `_namespace.host`; Gateway-API hostname VAPs pin routes to the stage apex.
   Caveat: at v1.5.0 the equivalent guard for legacy `networking.k8s.io/Ingress` objects does
   NOT exist (`ingress-hostname-policy.yaml` is main-only) — any tenant admin can claim any
   hostname on the shared root nginx. Single-operator cluster: accepted.
3. **resourceQuotas/schedulingClass** as first-class per-stage knobs (unused here so far; note
   the LimitRange-only-with-quotas coupling).
4. **Per-stage RBAC groups** (`tenant-<ns>-{view,use,admin,super-admin}`) — ACTIVE since
   platform OIDC was re-enabled 2026-07-06 to gate cluster-admin access (disabled 2026-07-05
   to 2026-07-06; see `platform.yaml`). The groups render into the cozy realm again; whether
   to actually bind operators to them is a separate decision — current access model is the
   single `cozystack-cluster-admin` group.
5. **Subtree lifecycle**: child namespaces are ownerReference'd to the parent — deleting a
   stage tenant garbage-collects everything in it (double-edged; see deletion bugs below).

What it does NOT buy: per-stage monitoring/ingress/etcd without real cost (a monitoring-enabled
tenant is ~20 pods/10 PVCs; three of those on a 3-node cluster is prohibitive — inherit
tenant-root's instead, accepting that metric visibility rolls up to the parent), and no
first-class sibling peering (#2656) — any deliberate cross-stage or shared-service traffic is
hand-written CCNP pairs.

## The depth-2 label bug — live on this cluster, fixed only on unreleased main

v1.5.0 `packages/apps/tenant/templates/namespace.yaml:78-84` derives ancestor labels from
dash-prefixes of the **parent namespace name**. `tenant-root` is not a name-prefix of
`tenant-guardian`, so only depth-1 tenants (whose parent IS tenant-root) get
`tenant.cozystack.io/tenant-root`. Depth ≥ 2 tenants never do — which breaks the generated
`tenant-root-egress` CCNP (tenant-root pods may egress only to namespaces labeled
`tenant.cozystack.io/tenant-root`), black-holing the **shared root ingress-nginx → grandchild
tenant backends** path.

- **Verified live 2026-07-06**: `tenant-guardian` (depth 1) carries the tenant-root label;
  `tenant-guardian-beta` (depth 2) does not.
- **Verified not fixed in v1.5.2 by content** (`git diff v1.5.0 v1.5.2 -- packages/apps/tenant`
  touches only `etcd/ingress/monitoring.yaml` dependsOn + a test): the fix — helper
  `tenant.ancestorTenantLabels`, which unconditionally emits the tenant-root label plus the full
  own-name prefix chain, commit `1ac9398a` "fix(tenant): emit full ancestor label chain on
  tenant namespaces" — exists only on main; `git tag --contains` is empty and no tag newer than
  v1.5.2 exists as of 2026-07-06. Watch for it in the next 1.5.x/1.6 release.
- **This bug is why `deployments/iam/*/networkpolicy.yaml` hand-writes the per-stage
  `tenant-guardian-<stage>-allow-root-ingress-keycloak` / `tenant-root-ingress-egress-
  keycloak-<stage>` CCNP pairs.** They are the compensating patch, not redundant boilerplate.
  After upgrading to a release containing `1ac9398a`, the generated policies admit tenant-root
  → descendant traffic natively; the hand-written **egress** halves become droppable. The
  **ingress** holes into Keycloak pods must stay regardless: our own `CiliumNetworkPolicy
  keycloak` flips those pods to default-deny, and its allow-list is the complete ingress
  contract.

## Other v1.5.x facts that shape how stages should be run

- **v1.5.2 is worth taking for the cold-install fix**: tenant-module HRs (etcd/ingress/
  monitoring) gained `dependsOn: victoria-metrics-operator` (cherry-pick of `6db5ae9c`),
  killing the VM*-CR-vs-admission-webhook rollback loop on cold bootstrap. With all our tenant
  flags false the exposure is theoretical, but cold-boot (dark drill) is exactly when it bites.
- **Tenant churn is fragile on 1.5.x**: deletion pre-delete hook runs unpinned
  `bitnami/kubectl:latest` (catalog discontinued; not in our `images.lock`, so guaranteed-broken
  in dark mode); upstream wedge modes #3018 (namespace stuck Terminating), #2961 (etcd-operator
  hang on tenant re-creation), #2473. Stage tenants should be **static and long-lived**; do not
  design flows that delete/recreate tenants (per-PR preview environments as tenants would be
  exactly the wrong shape — the existing single `previews` tenant with Deployments inside it is
  the right shape).
- **Naming law**: tenant names cannot contain dashes (`_helpers.tpl` hard-fail). Any
  per-application tenant scheme has to squeeze app names into single words
  (`verself-runner` → `verselfrunner`), and nesting app×stage tenants
  (`tenant-guardian-iam-beta`) puts every workload at depth 3 — deeper into the label-bug
  regime v1.5.x doesn't handle, closer to the 63-char namespace limit, and each app×stage
  cell needs its own hand-written peering to reach shared services. Sibling-stage tenants at
  depth 2 with pod-selector CiliumNetworkPolicies for intra-stage app tightening (the current
  iam pattern) is the shape the chart's mechanics actually support.
- `policy.cozystack.io/allow-to-apiserver: "true"` opens 6443 only (#2773) — in-tenant
  clients that talk to the API via :443 (kubernetes.default) break subtly.
- Monitoring inheritance means stage metrics land in tenant-root's VictoriaMetrics and are
  visible only there — consistent with our single-pane `tenant-root` observability stack; a
  future paying-customer tenant that must not see platform metrics (or whose metrics we must
  not mix) is the first legitimate trigger for a per-tenant `monitoring: true`.
