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

- Bundle completeness and offline verifiability gate releases; the signed lock
  covers the bundle (see `docs/supply-chain-design.md`).
- Proposals to drop or shrink the dark tier for simplicity are rejected by
  default; the burden of proof sits with the cut, not the tier.
- Registry outages and upstream rate limits are product bugs here only insofar
  as they break bundle builds — the runtime cluster's independence from them is
  the point.
