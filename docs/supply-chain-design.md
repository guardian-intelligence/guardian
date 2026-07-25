# Supply-chain design: signing, SBOM, and the trust model

Status: active as of 2026-07-03; reshaped 2026-07-22 — signing follows the
publication boundary, so only what Guardian releases to users (the
postflight CLI) is signed; cluster-internal images are governed by the
merge gate and the admission backstop, not signatures. Complements
`adrs/0003-validate-rendered-manifests.md` (Git-time invariants),
`adrs/0010-two-release-signatures-one-format-per-lane.md` (the release
signing model) and the cold-boot runbook (offline consumption).

## The trust model in one paragraph

The root of trust is Git plus CI as the canonical builder: the only path to
an `edge` tag on `ghcr.io/guardian-intelligence/*` is a merge to reviewed
main history (the `images` workflow publishes on main pushes only), and the
admission backstop requires every image the cluster runs to be digest-pinned
from an allowlisted registry. Signatures exist where a consumer outside that
loop needs them — the released postflight CLI: a cosign keyless signature
bound to the GitHub Actions OIDC workload identity via Fulcio and logged in
Rekor, attesting "the reviewed main history of
guardian-intelligence/guardian built this", plus Guardian's own
countersignature at the publication boundary. There are no signing keys in
CI: nothing to store, rotate, leak, or custody. Registries (ghcr.io
included) are untrusted distribution; a verifier checks the signature
identity, not the registry it pulled from. The running system — a dark cold
start included — only ever verifies. Bring-up needs no signing capability,
just the pinned identities and the Sigstore trusted root, both of which
travel with the drive.

## Canonical identities

Verifiers MUST pin these exact identity strings (OIDC issuer
`https://token.actions.githubusercontent.com` for both):

- `postflight-cli` binaries, OCI artifact + SBOM attestations:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main`
- images.lock signature bundles:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/images-lock-sign.yml@refs/heads/main`

The in-cluster countersigner (`docs/registry-design.md`) carries the same
list as its identity map: it refuses to countersign any digest that does not
verify against its repo's canonical identity, so adding a released artifact
means updating this list, the countersigner map, and creating the new
workflow file together.

Identities are per-workflow-file by construction — a new released artifact
gets a new workflow file, never a stanza in an existing one, so no single
identity can sign more than one artifact family.

## What gets signed and where it lives

| Artifact | Producer | Signature/attestation | Location |
|---|---|---|---|
| `postflight-cli` binaries + OCI artifact | `postflight-cli-image` workflow on main | per-binary cosign keyless sign-blob bundles (travel inside the artifact layer), cosign keyless signature + SPDX SBOM attestation on the artifact digest | ghcr.io, attached to the digest; bundles republished with each GitHub Release |
| every released digest (the release manifest) | in-cluster countersigner (`docs/registry-design.md`) | Guardian release countersignature (`openbao://guardian-images`, Rekor-logged, inclusion proof embedded in the bundle) as an OCI 1.1 referrer, minted only after the digest's Fulcio signature re-verifies | zot, attached to the digest; projected to ghcr by the release projector |
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
drive is unaffected — workloads deploy fresh from the manifests, so no
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
moves a pin; when it merges, CI on main builds and pushes the new `edge`
digest, which makes it *eligible* for promotion — nothing more. Every
digest behind `edge` came from reviewed main history by construction
(`images.yml` publishes on main pushes only), so a pin move needs no
per-digest verification.

First-party workload pins move by Flux image automation
(deployments/guardian/imageops): an ImagePolicy with digest reflection
follows each `edge` tag, and the ImageUpdateAutomation commits the
tag@digest bump straight to main as the `guardian-promotions` GitHub App,
through the main-protection ruleset's bypass. Post-push, the conformance
suite still runs on main (loud-after-merge), the admission policy enforces
digest pinning at apply time, and Flagger gates the rollout where a Canary
exists. Third-party workload images are ordinary dependencies: Renovate
proposes their bumps as reviewed PRs. The one Kargo lane left is the
postflight CLI release train, whose Stage opens channel-pin PRs that the
`promotion-automerge` workflow arms.

The ruleset (required checks + the bot bypass) is the enforcement's
load-bearing half and lives outside Git — re-assert it when recreating the
repo: `gh api repos/<owner>/<repo>/rulesets` with required check
`build-and-test` and the guardian-promotions App as bypass actor, plus
allow-auto-merge.

This deliberately weakens the old invariant "pin == the digest CI builds
from this same commit" to "pin ∈ digests published from main history".
What is bought: content merges stop requiring a round-trip repin commit,
and promotion becomes an automatable, independently-gated step per
service. What is given up: Git alone no longer proves the pin is the
*latest* build — freshness is the automation's job — and pinning an older
published digest (rollback) remains a legitimate one-line operation rather
than a violation.

## Verification

Online (any machine):

```sh
cosign verify \
  --certificate-identity "<artifact identity above>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/guardian-intelligence/postflight-cli@sha256:<pinned>

cosign verify-attestation --type spdxjson \
  --certificate-identity "<artifact identity above>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/guardian-intelligence/postflight-cli@sha256:<pinned>
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
(base/admission/image-provenance.yaml) requires every container
image in the rendered workload namespaces (tenant-guardian*,
guardian-analytics, guardian-cockpit, postflight-runner) to be digest-pinned and to start with
an allowlisted registry prefix (param ConfigMap
`tenant-guardian/guardian-image-provenance-params`; extending it is a
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

## Decision: no Transit (or any KMS) in the CI signing path

OpenBao Transit could hold a company-wide signing key (`cosign --key
openbao://…` is native). We deliberately keep it out of CI's signing path:

1. **Coupling**: the factory is CI. A Transit-held key means CI must reach
   OpenBao to sign, so artifact production halts whenever the management
   cluster is down — the same circularity as hosting the bootstrap registry
   inside the cluster it bootstraps, one level up.
2. **Exposure**: it puts a network path from public CI runners to the
   secret store that exists for cluster-internal custody.
3. **No benefit there**: CI signing wants a verifiable *builder identity*,
   and the builder is CI. Keyless binds exactly that.

The in-cluster **countersigner** sits on the other side of all three
reasons and is the deliberate exception: it runs inside the cluster (no CI
coupling, no public-runner network path), and its signature states
something keyless cannot — a Guardian-held key, under the custody model,
vouches for the digest after re-verifying the Fulcio original. That second
signature is Guardian's release signature: anything Guardian publishes
verifies with stock cosign against a plain public key, every signing event
is recorded in the Rekor transparency log, and the bundle embeds the
inclusion proof — so fully offline verification (key plus the pinned
trusted root) needs no GitHub identity or Sigstore-TUF freshness coupling.
CI's signatures remain the merge-gate currency; the countersignature is
the one Guardian owns outright.

## Exit path (sovereignty upgrade)

Keyless signatures name GitHub identities. The countersigner is the first
step off that dependency: every released first-party digest also carries a
signature by a Guardian-held key. The invariant sits at the publication
boundary — nothing Guardian releases to a public marketplace ships without
a verified Guardian signature, held by the release projector
(`docs/registry-design.md`). It is deliberately not
a runtime invariant: no pod is required to run a Guardian-signed image,
because what runs is already governed by the merge gate and the provenance
VAP. If Guardian later self-hosts its factory, images born in-cluster sign
with the same Transit-held key at build time and the countersigner's role
collapses into the builder. Old signatures remain valid statements about
who built what. Until then, registries stay untrusted, Git stays the
source of truth, and the dark bundle stays the registry-independence tier.
