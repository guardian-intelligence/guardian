# Supply-chain design: signing, SBOM, and the trust model

Status: active as of 2026-07-03 (PR for task: SBOM attestation + images.lock
signing). Complements `manifest-conformance-design.md` (Git-time invariants)
and the cold-boot runbook (offline consumption).

## The trust model in one paragraph

The root of trust is Git plus a reproducible build: every first-party image
digest is a pure function of a reviewed commit, enforced by the
`company-site-image` workflow (pin == digest CI builds from the same commit).
Signing adds an identity attestation on top — "the reviewed main history of
guardian-intelligence/guardian built this" — using cosign keyless signatures
bound to the GitHub Actions OIDC workload identity via Fulcio and logged in
Rekor. There are no signing keys anywhere: nothing to store, rotate, leak, or
custody. Registries (ghcr.io included) are untrusted distribution; a
verifier checks the signature identity, not the registry it pulled from.

## Canonical identities

Verifiers MUST pin these exact identity strings (OIDC issuer
`https://token.actions.githubusercontent.com` for both):

- Container images + SBOM attestations:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/company-site-image.yml@refs/heads/main`
- images.lock signature bundles:
  `https://github.com/guardian-intelligence/guardian/.github/workflows/images-lock-sign.yml@refs/heads/main`

## What gets signed and where it lives

| Artifact | Producer | Signature/attestation | Location |
|---|---|---|---|
| `company-site` image | `company-site-image` workflow on main | cosign keyless signature + SPDX SBOM attestation (`--type spdxjson`) | ghcr.io, attached to the digest |
| `images.lock` | `images-lock-sign` workflow on main pushes touching the lock | `cosign sign-blob --bundle` (embeds Fulcio cert + Rekor proof), pushed with `oras push` so the layer carries a filename title | `ghcr.io/guardian-intelligence/supply-chain:images.lock-<sha256>` and `:images.lock-latest` (package stays private — only authenticated drive builds fetch it; dark bring-up reads it from the drive) |

The dark-uplink haul is *derived* from `images.lock` and every blob in it is
digest-addressed, so a verified lock plus hash verification of the haul
against it covers the entire bundle. Signing the haul itself would add no
integrity the lock signature does not already provide. The chain is
enforced twice: `aspect infra bundle` refuses to build a drive whose lock
CI never signed, verifies the signature (pinned identity + pinned Sigstore
trusted root at `src/infrastructure/bootstrap/bundle/sigstore-trusted-root.json`),
and runs `bundle --verify` (lock/haul/hauler-manifest hash bindings); the
operator repeats both checks offline as step 0 of dark bring-up (see the
cold-boot runbook). The residual trust in the haul→manifest binding is the
custody model itself: the drive is custody, assembled and carried by the
operator who also holds the seal key — signatures defend the Git-derivation
and upstream-registry axes, not the operator.

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
of months). After verifying the lock, verify the haul against it (blob
digests are checked by hauler at load; the lock conformance test pins the
manifest side).

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
