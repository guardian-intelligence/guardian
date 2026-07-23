# Dependency management: proposers, tiers, and rollout safety

See `supply-chain-design.md` for the first-party image trust model,
`adrs/0003-validate-rendered-manifests.md` for Git-time invariants, and
`renovate.json5` for executable policy.

## One proposer per pin

Every pin has exactly one untrusted proposer; CI and the main-protection
ruleset decide every merge:

- **Renovate** owns source-plane pins configured in `renovate.json5` —
  including the third-party workload images (Keycloak, Electric) its
  kubernetes manager is scoped to.
- **Flux image automation** (`deployments/guardian/imageops`) owns the
  first-party workload pins that carry `$imagepolicy` setter markers; it
  commits straight to main as `guardian-promotions[bot]` through the
  ruleset bypass. Never hand-roll a pin bump on a marked line.
- **Kargo** owns only the postflight CLI release channels
  (`src/products/postflight-cli/release/channels.yaml` plus the release
  manifest's CLI lane). Never hand-roll or duplicate its promotion PRs.

Renovate configuration errors do not fail scheduled runs, so
`//:renovate_config_test` validates the config in the universal Bazel gate.

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

1. CI gates (automatic): the universal Bazel gate — `test //...` carries
   the source-policy/version-skew tests, the actions-allowlist and
   renovate-config checks, and fresh-download verification of every
   lockfile entry on both platforms (`//src/tools:multitool_lock_test`) —
   plus the secret scan and, on tool-pin diffs, `//src/tools:pins`
   fetch/unpack verification.
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
roundtrip, cosign offline bundle verify, k6 local run). Gitleaks already gets
real exercise through the secret scan on every PR.

## The Actions allowlist (lockstep or startup_failure)

The repo runs `allowed_actions: selected` with exact-digest patterns.
`.github/actions-allowlist.json` is the declared source of truth, and
`//:actions_allowlist_test` fails any PR whose workflows use a third-party
ref the file does not carry. Bumping a third-party action digest is a
two-step lockstep:

1. In the PR: update the workflow pin **and** the allowlist entry (drop the
   superseded digest).
2. On merge (repo admin): re-apply the setting —
   `gh api -X PUT repos/guardian-intelligence/guardian/actions/permissions/selected-actions --input .github/actions-allowlist.json`

Skipping step 2 causes a GitHub `startup_failure` before any job exists.
First-party `actions/*` refs are exempt through `github_owned_allowed`.
