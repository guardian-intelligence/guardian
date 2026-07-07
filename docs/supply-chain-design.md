# Supply-chain design: signing, SBOM, and the trust model

Status: active as of 2026-07-03 (PR for task: SBOM attestation + images.lock
signing; updated same week for promotion decoupling — pin moves are
provenance-verified, not rebuild-verified). Complements
`manifest-conformance-design.md` (Git-time invariants) and the cold-boot
runbook (offline consumption).

## The trust model in one paragraph

The root of trust is Git plus CI as the canonical builder: a deployment pin
may only move to a digest that CI built, pushed, and cosign-signed from
reviewed main history, enforced by the `company-site-image` workflow — a
pin-changing PR must name a digest carrying the canonical identity's
signature (verified without a rebuild), and content PRs do not move pins at
all. The signature is therefore the gate, not an add-on: a cosign keyless
signature bound to the GitHub Actions OIDC workload identity via Fulcio and
logged in Rekor, attesting "the reviewed main history of
guardian-intelligence/guardian built this". There are no signing keys
anywhere: nothing to store, rotate, leak, or
custody. Registries (ghcr.io included) are untrusted distribution; a
verifier checks the signature identity, not the registry it pulled from.

## Canonical identities

Verifiers MUST pin these exact identity strings (OIDC issuer
`https://token.actions.githubusercontent.com` for both):

- `company-site` image + SBOM attestations:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/company-site-image.yml@refs/heads/main`
- `analytics-ingest` image + SBOM attestations:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/analytics-ingest-image.yml@refs/heads/main`
- `alert-relay` image + SBOM attestations:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/alert-relay-image.yml@refs/heads/main`
- images.lock signature bundles:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/images-lock-sign.yml@refs/heads/main`

Identities are per-workflow-file by construction — a new first-party image
gets a new workflow file, never a stanza in an existing one, so no single
identity can sign more than one artifact family.

## What gets signed and where it lives

| Artifact | Producer | Signature/attestation | Location |
|---|---|---|---|
| `company-site` image | `company-site-image` workflow on main | cosign keyless signature + SPDX SBOM attestation (`--type spdxjson`) | ghcr.io, attached to the digest |
| `analytics-ingest` image | `analytics-ingest-image` workflow on main | cosign keyless signature + SPDX SBOM attestation (`--type spdxjson`) | ghcr.io, attached to the digest |
| `alert-relay` image | `alert-relay-image` workflow on main | cosign keyless signature + SPDX SBOM attestation (`--type spdxjson`) | ghcr.io, attached to the digest |
| union images lock (generated) | `images-lock-sign` workflow on main pushes touching any union input (declared lock, manifest trees, the imageset tool) | derives the union with `//src/infrastructure/cmd/imageset`, then `cosign sign-blob --bundle` (embeds Fulcio cert + Rekor proof), pushed with `oras push` so the layer carries a filename title | `ghcr.io/guardian-intelligence/supply-chain:images.lock-<sha256>` (one tag per union hash, no floating tag; package stays private — only authenticated drive builds fetch it, dark bring-up reads it from the drive) |

The artifact inventory itself is split and mostly generated:
`src/infrastructure/bootstrap/bundle/images.declared.lock` hand-declares
only what no repo manifest renders (bootstrap artifacts, Talos/k8s system
images, operator-spawned workloads, Go-tool-referenced job images), and
`//src/infrastructure/cmd/imageset` derives the complete **union lock** by
adding every digest-pinned image ref extracted from the manifest trees. The
derivation is a pure function of the checkout — CI, drive builds, and
offline operators all reproduce identical bytes from the same revision.

The union is revision-exact by design: it contains what the checkout
declares and renders, nothing more. The retired hand-maintained lock
instead *accumulated* superseded pins so an in-flight blue/green window or
a Git-revert rollback stayed mirror-servable from one haul. That property
is deliberately traded away: a dark rollback now means building (or
retaining) a bundle at the revision being rolled back to, and a standing
air-gapped cluster doing live upgrades must keep its mirror store additive
across syncs rather than serving a single revision's haul. Bring-up from a
drive is unaffected — Flagger deploys fresh from the manifests, so no
superseded digest is ever needed.

The dark-uplink haul is *derived* from the union lock and every blob in it
is digest-addressed, so a verified union plus hash verification of the haul
against it covers the entire bundle. Signing the haul itself would add no
integrity the union signature does not already provide. The chain is
enforced twice: `aspect infra bundle` derives the union, refuses to build a
drive whose union CI never signed, verifies the signature (pinned identity
+ pinned Sigstore trusted root at
`src/infrastructure/bootstrap/bundle/sigstore-trusted-root.json`),
and runs `bundle --verify` (union/haul/hauler-manifest hash bindings); the
operator repeats the checks offline as step 0 of dark bring-up (see the
cold-boot runbook), re-deriving the union from the checkout and
byte-comparing it against the drive copy. The offline re-derivation runs
the drive-carried `imageset-bin`, so its binding of drive bytes to checkout
bytes holds under the custody model (the drive and its binaries travel
with the operator, like the seal key) — the cosign check is what defends
the Git-derivation axis, proving the union was produced from reviewed main
history. The residual trust in the haul→manifest binding is the
custody model itself: the drive is custody, assembled and carried by the
operator who also holds the seal key — signatures defend the Git-derivation
and upstream-registry axes, not the operator.

## Promotion: how digests move

Deployment pins are decoupled from content changes. A content PR never
moves a pin; when it merges, CI on main builds, pushes, and signs the new
digest, which makes it *eligible* for promotion — nothing more. Promotion
is a separate pin-only PR that bumps the manifest pin and nothing else:
the inventory is the generated union, so the moved pin joins it by
derivation, not by a second edit. The `site-gate` check verifies exactly
two things on that PR: the proposed digest carries a cosign signature by
the canonical image identity above (seconds, no rebuild — the gate
classifies the diff and skips the build on pin-only PRs), and the
conformance tests still pass.

The promoter is Kargo (deployments/guardian/promotion): the Warehouse
tracks the digest behind `company-site:edge`, and the prod Stage's
promotion opens the pin-bump PR as the `guardian-promotions` GitHub App.
The `promotion-automerge` workflow arms automerge on the bot's PRs; no
inventory sync exists or is needed. The promoter is untrusted by
construction: branch protection requires `build`
and `site-gate` on main, so nothing reaches a pin that CI did not sign
from main history, whether the PR was opened by a human or the bot. The
required-checks + allow-auto-merge repo settings are the enforcement's
load-bearing half and live outside Git — re-assert them when recreating
the repo (exact commands in TRIBAL_KNOWLEDGE.md).

This deliberately weakens the old invariant "pin == the digest CI builds
from this same commit" to "pin ∈ digests the canonical identity has
signed". What is bought: content merges stop requiring a round-trip repin
commit, and promotion becomes an automatable, independently-gated step per
service. What is given up: Git alone no longer proves the pin is the
*latest* build — freshness is the promoter's job — and pinning an older
signed digest (rollback) remains a legitimate one-line operation rather
than a violation.

## Verification

Online (any machine):

```sh
cosign verify \
  --certificate-identity "<image identity above>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/guardian-intelligence/company-site@sha256:<pinned>

cosign verify-attestation --type spdxjson \
  --certificate-identity "<image identity above>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/guardian-intelligence/company-site@sha256:<pinned>
```

Offline (dark drive): the sign-blob bundle JSON embeds the certificate and
the Rekor inclusion proof, so `cosign verify-blob --bundle
images.lock.sigbundle --certificate-identity "<lock identity above>" …` needs
no network — only the Sigstore trusted root, which the dark drive carries as
a pinned file (refresh it when refreshing the drive; it rotates on the order
of months). After verifying the drive's union lock, re-derive the union from
the checkout and byte-compare, then verify the haul against it (blob
digests are checked by hauler at load; the conformance tests pin the
manifest side).

## Admission backstop (ValidatingAdmissionPolicy)

Git-side enforcement covers what the repo renders; the in-cluster backstop
covers what actually reaches the apiserver. `guardian-image-provenance`
(base/app-patches/image-provenance-admission.yaml) requires every container
image in the rendered workload namespaces (tenant-guardian*,
guardian-analytics, verself-runner) to be digest-pinned and to start with
an allowlisted registry prefix (param ConfigMap
`tenant-guardian/guardian-image-registry-allowlist`; extending it is a
supply-chain decision made in its own reviewed PR). It matches workload
templates as well as Pods so a violation fails the Flux apply synchronously
— the CEL message lands verbatim in the Kustomization status — rather than
admitting cleanly and failing later in ReplicaSet events. VAP is in-process
apiserver CEL: no webhook, no availability dependency in the DR path.

The declared lock is projected into the policy's param ConfigMap
(kustomize `configMapGenerator` over `images.declared.lock` — the file in
git IS the param), so operator-spawned images that reach admission as tag
refs pass by exact declared entry (tag+digest form, e.g. the CNPG images
Cozystack's managed-app machinery spawns in the stage tenants). Everything
else must be digest-pinned from an allowlisted prefix. Admitting a new
operator image is therefore the same reviewed act as any dependency
change: a one-line declared-lock PR.

Enforcement is `Deny`: a violation fails the apply (Flux surfaces the CEL
message in the Kustomization status) or the pod creation (operator
controllers retry against the same denial). The vap-denial-canary asserts
every 10 minutes that the policy still flags a violating probe — pages
critical on silence, since the apiserver exposes no per-policy VAP metrics
on this cluster (verified 1.34.3).

## Decision: no Transit (or any KMS) in the signing path

OpenBao Transit could hold a company-wide signing key (`cosign --key
hashivault://…` works against OpenBao). We deliberately do not do this:

1. **Coupling**: the factory is CI. A Transit-held key means CI must reach
   OpenBao to sign, so artifact production halts whenever the management
   cluster is down — the same circularity as hosting the bootstrap registry
   inside the cluster it bootstraps, one level up.
2. **Exposure**: it puts a network path from public CI runners to the
   secret store that exists for cluster-internal custody.
3. **No benefit**: signing wants a verifiable *builder identity*, and the
   builder is CI. Keyless binds exactly that. Transit's job in guardian is
   data-encryption custody for disaster recovery, not artifact signing.

## Exit path (sovereignty upgrade)

Keyless signatures name GitHub identities. If Guardian later self-hosts its
factory, the upgrade is a verification-policy change, not a re-architecture:
run a private Sigstore (or switch to a key held under the custody model,
used only by the self-hosted CI), re-sign the current pins, and update the
identity strings here and in the verify steps. Old signatures remain valid
statements about who built what. Until then, registries stay untrusted,
Git stays the source of truth, and the dark bundle stays the
registry-independence tier.
