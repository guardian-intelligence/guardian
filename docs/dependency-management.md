# Dependency management: proposers, tiers, and rollout safety

Status: active as of 2026-07-11. Complements `supply-chain-design.md` (the
trust model for first-party images) and
`adrs/0003-validate-rendered-manifests.md` (Git-time invariants). Policy
lives in `renovate.json5`; this doc is the operating manual.

## One proposer per pin

Every pin has exactly one proposer, and both proposers are untrusted — CI
plus branch protection decide every merge:

- **Renovate** proposes the source plane: Bazel tool archives
  (`src/tools/*/*.MODULE.bazel`), `bazel_dep` and `oci.pull` in
  `MODULE.bazel`, `go.mod`, the pnpm catalog, GitHub Actions digests, tofu
  provider pins, the bootstrap pivots (`scripts/bootstrap/`), the Talos
  installer image, the cozy-installer platform pin, the Flux
  source-controller chart pins and infrastructure HelmRelease image pins
  (the scoped `flux` manager), and Renovate itself.
- **Kargo** proposes the stage-manifest trees its Stage pipelines
  git-push to (`deployments/{company,iam,products}/**` — `ignorePaths`
  for Renovate, with the helm-values/kustomize managers disabled
  outright). Never hand-roll or duplicate its promotion PRs.

The scheduled proposer pages if its run fails (`renovate.yml`); a config
error does not fail the run, so `renovate-config.yml` validates
`renovate.json5` on every PR that touches it.

## Trust tiers

| Tier | Examples | Policy |
| --- | --- | --- |
| First-party actions | `actions/*`, `renovatebot/*` | no cooldown |
| Standalone tools & libs | npm catalog, go.mod, debug CLIs | 3-day cooldown (mirrors pnpm `minimumReleaseAge`) |
| Cluster-coupled tools | talm, talosctl, flux CLI, boot-to-talos | one grouped PR, never merges ahead of the paired substrate move (version-skew test enforces) |
| Substrate doorbells | kubectl+kubernetesVersion, Talos installer, cozy-installer, tofu providers | the PR **starts** the upgrade runbook; merging it is the runbook's **last** step, so Git never claims a version the cluster does not run |

Majors additionally wait for a dependency-dashboard click. Security PRs
(OSV) bypass cooldown and carry the `security` label.

No automerge anywhere yet: tiers earn automerge after watched clean cycles,
the same arc as the image-provenance VAP Warn→Deny flip. Graduation
criteria per tier: ≥3 consecutive clean cycles (created → CI green → merged
→ converged with no manual fixup), and only for tiers whose CI gates
actually exercise the artifact.

## Due diligence per PR

1. CI gates (automatic): build, the manifest/version-skew conformance suite
   (runs on any `src/infrastructure/**` **or** `src/tools/**` diff), tool-pin
   fetch/unpack verification (`//src/tools:pins`), secret scan, actions
   allowlist check.
2. Review (human/agent): read the upstream changelog Renovate embeds; for
   anything cluster-coupled or substrate, that means the release notes, not
   the diff summary.
3. Post-merge (the real gate for cluster tools): babysit Flux convergence
   and the affected drills. Pre-merge CI cannot validate a cluster-facing
   CLI's behavior; do not read green checks as behavioral validation.

TODO: replace fetch/unpack verification with a real binary vetting
pipeline — upstream signature/attestation verification per tool (most of
`src/tools` ships sigstore signatures or signed checksums) and hermetic
behavioral exercise where the tool's real job runs offline (restic repo
roundtrip, cosign offline bundle verify, k6 local run). talm and gitleaks
already get real exercise (render suite, secret scan on every PR).

## The Actions allowlist (lockstep or startup_failure)

The repo runs `allowed_actions: selected` with exact-digest patterns.
`.github/actions-allowlist.json` is the declared source of truth, and
`check-actions-allowlist.sh` fails any PR whose workflows use a third-party
ref the file does not carry. Bumping a third-party action digest is a
two-step lockstep:

1. In the PR: update the workflow pin **and** the allowlist entry (drop the
   superseded digest).
2. On merge (repo admin): re-apply the setting —
   `gh api -X PUT repos/guardian-intelligence/guardian/actions/permissions/selected-actions --input .github/actions-allowlist.json`

Skipping step 2 kills every workflow that uses the new digest as a
`startup_failure`: no jobs, no logs, and the in-workflow failure→ntfy page
never runs. `github_owned_allowed` covers `actions/*`, so first-party
action bumps need no lockstep. A standing read-only drift check between the
file and the live setting is future work, same pattern as the approved tofu
drift cron.
