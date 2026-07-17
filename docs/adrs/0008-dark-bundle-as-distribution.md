# 0008 — The dark bundle is product distribution, not just DR

Status: Accepted · Date: 2026-07-03

## Context

The dark-uplink bundle began as a disaster-recovery artifact: boot the cluster
with no upstream registries. Treated as DR-only, it is a standing temptation to
cut — it taxes every image change with lock discipline and mirror coverage.

## Decision

The dark bundle is a first-class product surface. When the repo is handed to
other users, the bundle is how a system this complex is guaranteed to arrive
exactly as dogfooded — the same digests, charts, and artifacts, verifiable
offline. It is also the entry ticket to air-gapped and sovereign deployments, a
large addressable market whose only cost is the supply-chain discipline the
platform wants anyway.

## Consequences

- Lock discipline gates every merge (digest-pinned, declared/rendered disjoint,
  derivable union); offline verifiability is enforced when the bundle is built
  and at dark bring-up; completeness is provable only positively, by the
  cold-boot drill. The signed lock covers the bundle (see
  `docs/supply-chain-design.md`).
- Proposals to drop or shrink the dark tier for simplicity are rejected by
  default; the burden of proof sits with the cut, not the tier.
- Registry availability is a first-class runtime concern in its own right:
  steady-state pulls ride the in-cluster zot pull-through mirror with permanent
  fallback to upstream, and mirror outages page. The dark bundle is the
  registry-independence tier — the guarantee that the platform can arrive and
  boot with no upstream at all — not the everyday pull path (`darkBundleMirror`
  stays off in steady state).

Related source: `src/infrastructure/bootstrap/bundle/images.declared.lock`,
`src/infrastructure/tests/images_lock_test.go`,
`src/infrastructure/talm/values.yaml`
