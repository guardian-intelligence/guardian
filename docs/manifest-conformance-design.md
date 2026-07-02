# Manifest conformance (Tier 1) — design

Status: **IMPLEMENTED for Tier 1 semantic checks; Stage A/B rollout in
progress.** The runtime counterpart is the platform convergence proof
(`aspect infra converged`), which reads live Flux Kustomization conditions;
component design for the OpenBao secrets platform lives in
`docs/openbao-design.md`.

Principle: validate the **rendered artifact that ships**, against schema and against the real
API server's admission — never source templates, never hand-restated field values. Tier 1
owns three failure classes: structural validity, CRD-schema validity, admission/PSA
admissibility. Policy-as-code (Kyverno) is a later tier. This replaces ~770 brittle
pinned-value Go assertions.

- **Stage A** (offline, hermetic, every PR, in `bazel test //...`): per-overlay render
  (`kustomize build` / `flux build`) → `kubeconform -strict` against vendored core + CRD
  schemas (version-pinned, generated from the exact deployed CRDs + community catalog).
  **Fail on any un-allowlisted skip** (logging is not enough) so "skipped" never reads as
  "passed."
- **Stage B** (online, CI-gated): rendered output →
  `kubectl apply --server-side --dry-run=server --validate=strict`, against a **per-run
  ephemeral cluster seeded from the repo's own declared CRDs + PSA-labeled namespaces +
  ValidatingAdmissionPolicies + webhook configs + ResourceQuotas/LimitRanges + storage
  classes**. Chosen over a standing prod kubeconfig in CI (least dangerous; can't drift
  because it's built from the same manifests prod is). **Capture API warnings as test output;
  fail on unknown-field warnings.**
- **Helm expanded**: HelmRelease-backed components are rendered to expanded manifests via
  `helm install --dry-run=server` (faithful `.Capabilities`/`lookup` from the cluster), then
  pass through kubeconform + dry-run. Only charts where we inject non-trivial values are in
  scope. If a chart uses `lookup`, either seed exactly what prod has or ban `lookup`.
  Acknowledged ~95% fidelity vs Flux's in-cluster HelmController render; the last 5% (Flux
  value-merge/postRenderers/reconcile) is a later tier that runs the actual Flux controllers.
- **Custom Go tests survive only for cross-field semantic invariants** no schema/admission
  check can express (e.g. seal stanza `current_key_id` ↔ init-container filename agreement;
  referenced runbook exists). Per-field value checks become snapshots; "all resources must…"
  rules wait for the policy tier.

## Key references
- kubeconform: https://github.com/yannh/kubeconform
