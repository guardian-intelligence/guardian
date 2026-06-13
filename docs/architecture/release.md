# Release architecture

Status: design ratified 2026-06-12 (operator). Supersedes the self-hosted
runner POC (`.github/workflows/release.yml` + `docs/runbooks/release-runner.md`),
which keeps releases flowing until this lands, then retires with its standing
credentials. Companions: `docs/roadmap.md` (M6/M7),
`docs/runbooks/aisucks-release.md` (the gate spec the judge automates). This
doc changes by amendment.

## The authority split

Build authority and deploy authority never meet:

- **GitHub builds.** CI on the public mirror builds hermetically (Bazel),
  pushes images + a CUE release manifest to ghcr.io by digest, signs
  provenance with cosign **keyless** (GitHub OIDC; verifiers pin
  repo + workflow + ref), and advances the **edge** channel. GitHub holds a
  ghcr write token and nothing else — no cluster credential exists there,
  not "scoped", none.
- **The fleet deploys.** Each cluster runs Flux (source-controller +
  kustomize-controller only) pulling its channel: dev follows **edge**
  (continuous — dev tracking edge IS the "every site has a dev version"
  convention), gamma follows edge with judgment, prod follows **stable**.
  Flux verifies cosign signatures natively: keyless identity for edge, the
  fleet's pinned Transit public key for stable.
- Only the fleet's OpenBao Transit key advances stable or attests a gate
  verdict, so a fully compromised GitHub reaches gamma at worst — the
  designed sacrificial environment.
- Residual, recorded honestly: a malicious-but-*healthy* build that survives
  review passes gamma's soak — soak judges health, not intent. Backstop:
  review + reproducibility (anyone rebuilds the commit and matches the
  digest; proven on every release).

## Data model

- **Release manifest** (CUE → signed OCI artifact): monotonic `seq`
  (consumers refuse regressions — the pointer-replay defense; full TUF
  deliberately not adopted: the accepted residual is that a frozen registry
  can hide *new* releases, never serve old ones), tag, commit,
  `kind: feature | reliability` (budget-gate input), per-component image +
  manifest-bundle digests, notes.
- **Channels** = two tiny signed pointer artifacts: `edge` (CI-signed),
  `stable` (Transit-signed).
- **Attestations** (in-toto predicates via cosign referrers on the manifest
  digest): SLSA provenance (CI); `gate-pass` / `gate-fail` (judge, Transit);
  `rejected` (a taint, refused everywhere until explicit operator
  forgiveness — never a timeout); `deployed` (per site; audit trail and the
  status page's release-train source).
- **Manifests** move from Go templates to kustomize bases + three per-site
  overlays, substituting from a bootstrap-laid site ConfigMap; secrets stay
  in-cluster. guardian-up's render/push path retires for components Flux
  owns.

## The release judge (the only bespoke code)

One small per-site binary — a few hundred lines of policy; role from site
config. Everything mechanical (pull, verify, apply, health, prune) is Flux;
everything cryptographic is cosign/Transit; metric evaluation is
VictoriaMetrics queries against the M2 recording rules.

- **gamma (gate):** when Flux reports the edge candidate healthy, run the
  soak — 10m, from local VM: alerts quiet · probe_success == 1 · restart
  delta 0 · 5xx == 0 · hello synthetic passes → Transit-sign `gate-pass` →
  advance stable. Fail → pointer back, attest `gate-fail`, page. A newer
  edge during a soak aborts it without a verdict — latest wins on gamma.
- **prod (promote):** admit only a digest carrying provenance ∧ gate-pass ∧
  ¬tainted ∧ (budget remaining > 0 ∨ kind == reliability), strictly
  serialized; then a 15m post-promote watch (same criteria, prod's VM).
  Fail → pointer-move rollback to lastGood + taint + page; pass → attest
  `deployed`. Auto-rollback fires only inside the window; after it, manual
  only — automation blast radius stays capped.
- **Rollback is a pointer move.** Flux converges whatever the pointer
  names; lastGood images sit in the node image cache (and the per-site
  pull-through mirror once zot lands), so rollback never depends on ghcr
  being up. Cheap and idempotent — which is what makes the next clause safe:
- **Fail-open amendment (operator, 2026-06-12):** missing telemetry during
  the watch MAY trigger rollback. Promotion still fails closed (no advance
  without positive evidence), but rollback does not require positive
  evidence of failure. An observability outage can therefore roll prod back
  to a known-good digest — acceptable because the action is cheap, safe,
  and idempotent. Later: explicit liveness heartbeats make absence a
  first-class signal rather than an inference.
- **Degradation is stasis.** Registry unreachable → nothing changes, alert
  after N consecutive poll failures. Gamma down or mid-migration → stable
  never advances → prod safely static. Budget query failure → treated as
  exhausted (feature releases refuse, reliability passes), page.
- Judge state is minimal (`lastGood`, taints); converge state is Flux's
  Kustomization status. Crash recovery = re-read pointers + status.

## Substrate decisions

- **Flux**: source + kustomize controllers only — no helm, no
  image-automation. A platform dependency: rides the platform-release lane
  (nonprod before prod). Exit ramps recorded both ways: if the
  zero-cross-cluster-credential rule ever relaxes, hub tools (Kargo/Argo)
  reopen; if Flux bloats, the judge already owns all policy and a
  hand-rolled pull loop is the fallback.
- **Registry: ghcr.io now, zot at M7** (per-site pull-through mirror, then
  the vending registry behind the Gateway). **No Harbor.**
- **Flagger** is the weighted-canary path once M3 routes exist — it speaks
  Gateway API, so M6's weighted canaries become configuration on the Cilium
  routes, not bespoke traffic logic.
- **Flux and the judge deploy via bootstrap / guardian up, operator-only.**
  The pipeline never updates itself; the workstation is the second channel.
- Migrations discipline is unchanged and unverifiable by the pipeline:
  additive-only, enforced at review; pointer-move rollback assumes it.

## Sequencing

1. **CI lane**: `oci_push` targets, CUE manifest, keyless signing, edge
   pointer. First bridge landed for aisucks:
   `.github/workflows/public-release.yml` publishes the fleet-gated
   `//src/products/aisucks/services/api:publish_ghcr` image to GHCR by
   digest, cosign-signs it keyless, attaches SLSA/in-toto provenance, and
   can publish the npm SDK after package bootstrap. VERIFY: `cosign verify`
   of a real release from a clean machine (pulls part of M7's exit criteria
   forward).
2. **Flux on dev** following edge; the template→kustomize conversion lands
   here. VERIFY: a merge reaches dev converged in minutes, hands-off.
3. **Judge, gate role on gamma.** Prerequisite: bao Transit init on gamma
   (operator unseal custody, same practice as dev).
4. **Judge, promote role on prod** — the budget gate is M2's teeth arriving.
5. **Retire the runner** workflow + runbook; run M6's drills: a
   deliberately broken release refused at gamma, a synthetic budget
   exhaustion freezing a feature release, the auto-rollback drill restoring
   N-1 unattended, taint-and-forgive.

Out of scope here: the workload-plane stress test (1000-microVM burst) is
its own workstream with its own design; the workload agent ships through
this pipeline as an ordinary tenant when it arrives.
