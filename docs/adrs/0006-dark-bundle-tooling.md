# 0006 — Hauler builds and serves the dark bundle

Status: Accepted · Date: 2026-07-05

## Context

The dark tier (ADR 0008) needs one artifact that carries every image, chart, and
Flux OCI artifact the cluster runs, and can serve them as a plain registry mirror
during a cold boot with no uplink. Requirements: byte-identical round-trip (digests
in `images.lock` must survive save/load/serve), pure-Go so Bazel builds it from
source, and no second packaging format of our own.

## Decision

Hauler: `store save` / `load` / `serve` covers bundle build, transport, and
mirror-registry serving, and the round-trip is proven byte-identical against our
lock digests.

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

- Hauler is built from source under `src/tools/hauler` with module isolation; pin
  and patch rationale live as comments in its `go.mod`.
- OCI charts and Flux artifacts are added via `add image`, never `add chart` —
  `add chart` re-packages and breaks the digest the lock pins.
- The bundle's completeness is provable only positively: the cold-boot drill must
  account for every digest the cluster holds via the served registry's access logs.
