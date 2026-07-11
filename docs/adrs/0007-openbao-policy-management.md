# 0007 — OpenBao policy changes ride PRs, not a policy controller

Status: Accepted · Date: 2026-07-04

## Context

OpenBao policy and mount configuration changes as products grow, which invited a
standing reconciler to own it. Candidates evaluated: tofu-controller running the
Terraform Vault provider in-cluster, vault-config-operator, and CI-applied
Terraform with a privileged token.

## Decision

All three rejected: each adds standing privileged machinery — a long-lived
root-adjacent credential and a controller that can rewrite the secret plane — to
solve config churn that per-namespace scoping already eliminates. Policy truth
lives in the self-init container (`docs/secrets.md`): a new secret in an existing
scope is a routine PR; a structural change (new namespace or mount) is the
documented reinit ceremony (`src/infrastructure/runbooks/openbao-static-seal-self-init.md`).

## Consequences

- No component can mutate OpenBao policy at runtime; the blast radius of a
  compromised workload stays inside its namespace scope.
- Structural changes cost a ceremony instead of a reconcile. That price is paid
  rarely: scopes can be created before their namespace exists (roles bind by
  name), so most growth is routine.
- Revisit trigger: if namespace creation becomes routine (golden-path per-product
  tenants), move to label-selector/identity-templated policies — frequency of
  structural change is the signal, not convenience.
