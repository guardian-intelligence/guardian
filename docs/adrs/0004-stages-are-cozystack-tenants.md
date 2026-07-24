# 0004 — Deployment stages are Cozystack tenants

Status: Accepted · Date: 2026-06-27

## Context

Beta/gamma/prod need hard isolation from each other on one management cluster.
Cozystack's tenancy docs bless environments as tenants, but every upstream example
maps tenant→team; no public repo models a multi-stage tenant split — this repo is
the reference implementation of the pattern. The alternatives were plain namespaces
inside one tenant (shared openness unless per-namespace default-deny is hand-written)
or per-application tenants.

## Decision

Stages are child Tenants of `tenant-guardian` — prod and previews, since beta and
gamma were cut for prod-first delivery behind Flagger canaries (#704) —
declared in `src/infrastructure/deployments/guardian/system/stage-tenants.yaml`, each
with `spec.host: <stage>.guardianintelligence.org`; staged workloads deploy into the
derived `tenant-guardian-<stage>` namespaces. All service flags stay false: stages
inherit etcd/ingress/monitoring from `tenant-root` (a bare tenant is near-free; a
monitoring-enabled one is ~20 pods/10 PVCs, prohibitive per extra stage on a
3-node cluster).

What the tenant boundary buys over namespaces:

- **Sibling default-deny for free**: Cozystack's generated sender-egress
  CiliumClusterwideNetworkPolicies make sibling tenants unreachable in both
  directions with zero policy authoring.
- **Domain hierarchy**: `spec.host` propagates to every app in the stage.
- **Per-stage quotas, scheduling class, and RBAC groups** as first-class knobs.

Never model stages as per-application tenants: tenant names ban dashes, app×stage
nesting deepens into the ancestor-label regime and the 63-char namespace limit, and
every cell would need hand-written peering to shared services. Intra-stage app
tightening is pod-selector CiliumNetworkPolicies (the `deployments/iam` pattern).

Stage tenants are **static and long-lived**. Tenant deletion is a known-fragile
operation on the 1.5.x line (unpinned deletion-hook image, multiple upstream wedge
modes), so no flow may delete/recreate tenants — ephemeral per-PR workloads, if
they ever return, are Deployments inside an existing tenant, never tenants
themselves.

## Consequences

- There is no first-class sibling peering: any deliberate cross-stage or
  shared-service traffic is hand-written policy. Root ingress → stage backends is
  one entity-based ingress CCNP per stage (`fromEntities: [host, remote-node]`,
  the `deployments/iam/prod` pattern): the ingress controller runs hostNetwork, so
  endpoint-selector policies can never match it, and
  `src/infrastructure/tests/cilium_node_identity_test.go` bans that whole class.
- Stage metrics roll up into `tenant-root`'s monitoring; the first tenant whose
  metrics must not mix with the platform's (a paying customer) is the first
  legitimate trigger for a per-tenant `monitoring: true`.

Related source: `src/infrastructure/deployments/guardian/system/stage-tenants.yaml`,
`src/infrastructure/deployments/iam/prod/networkpolicy.yaml`,
`src/infrastructure/tests/cilium_node_identity_test.go`
