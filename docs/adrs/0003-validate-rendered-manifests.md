# 0003 — Validate rendered manifests, not source templates

Status: Accepted · Date: 2026-07-02

## Context

Manifest safety was ~770 Go assertions pinning individual field values in source
templates. Pinned-value tests restate the manifest by hand (drift in the test is
invisible), and source templates are not what ships — Flux renders overlays, Helm
expands charts. Three failure classes need Git-time coverage: structural validity,
CRD-schema validity, and admission/PSA admissibility.

## Decision

Validate the **rendered artifact that ships**, against schema and against a real API
server's admission — never source templates, never hand-restated field values.

- **Stage A** (offline, hermetic, every PR, in `bazel test //...`): per-overlay
  render (`kustomize build` / `flux build`) → `kubeconform -strict` against vendored
  schemas generated from the exact deployed CRDs. Any un-allowlisted skip **fails**,
  so "skipped" never reads as "passed".
- **Stage B** (online, CI-gated): rendered output →
  `kubectl apply --server-side --dry-run=server --validate=strict` against a per-run
  ephemeral cluster seeded from the repo's own CRDs, PSA-labeled namespaces,
  ValidatingAdmissionPolicies, webhook configs, quotas, and storage classes — chosen
  over a standing prod kubeconfig in CI (least dangerous, cannot drift: it is built
  from the same manifests prod is). API warnings are test output; unknown-field
  warnings fail.
- **HelmRelease-backed components** render via `helm install --dry-run=server` then
  flow through the same two stages; only charts with non-trivial injected values are
  in scope. Accepted ~95% fidelity vs Flux's in-cluster render — the last 5%
  (value-merge, postRenderers) is a later tier that runs the actual Flux controllers,
  as is policy-as-code.
- **Custom Go tests are authoritative only for derivation-backed semantic
  invariants that no schema/admission check can express.** Existing hand-restated
  fields and snapshots are migration debt: remove rather than update them when
  they block source changes.

The runtime counterpart is the platform convergence proof (`aspect infra converged`),
which reads live Flux Kustomization conditions.

## Consequences

- A manifest bug must survive rendering, schema, and real admission to reach the
  cluster; the test suite can no longer be wrong about a field the manifest changed.
- CI needs an ephemeral API server per run, and vendored schemas must be regenerated
  when CRDs move — that regeneration is part of any CRD bump.
- Charts using `lookup` must have their lookups seeded exactly or be banned.
