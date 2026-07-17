# 0006 — Hauler builds and serves the dark bundle

Status: Accepted · Date: 2026-07-05

## Context

The dark tier (ADR 0008) needs one artifact that carries every image, chart, and
Flux OCI artifact the cluster runs, and can serve them as a plain registry mirror
during a cold boot with no uplink. Requirements: byte-identical round-trip (digests
in `images.lock` must survive save/load/serve), pure-Go so Bazel builds it from
source, and no second packaging format of our own.

## Decision

Hauler: a lock-driven pipeline — the union `images.lock` is projected into a
hauler Images manifest, then `store sync` / `save` / `load` / `serve registry`
cover bundle build, transport, and mirror-registry serving — with the round-trip
proven byte-identical against our lock digests.

Rejected:

- **Zarf** — pre-1.0 with a breaking v1beta1 schema landing ~Oct 2026, CRC-suffixed
  repository names break digest identity, and its git-vendoring solves a problem we
  don't have once Flux sources from OCI.
- **skopeo sync** — CGO/gpgme makes static builds painful, and arbitrary-artifact
  fidelity has been an open upstream issue since 2020.
- **imgpkg** — Carvel is on life support.

Fallback if Hauler v2 disappoints: mindthegap (Nutanix, crane-based, actively
released), or a ~200-line repo-owned go-containerregistry command — the
architecture (lock-driven bundle, serve-as-mirror) survives either swap.

## Consequences

- Hauler is built from source under `src/tools/hauler` with module isolation —
  and patched (notably verbatim mirror repo paths, load-bearing for
  serve-as-mirror); pin rationale lives in its `go.mod`, patch rationale in the
  patch headers under `src/tools/hauler/patches/`.
- OCI charts and Flux artifacts enter the bundle as plain image entries in the
  projected manifest (`src/infrastructure/cmd/bundle`), never via
  `store add chart` — that re-packages and breaks the digest the lock pins.
- The bundle's completeness is provable only positively: the cold-boot drill must
  account for every digest the cluster holds via the served registry's access logs.

Related source: `src/infrastructure/cmd/bundle/main.go`,
`.aspect/tasks/infra.axl`, `src/tools/hauler/go.mod`
