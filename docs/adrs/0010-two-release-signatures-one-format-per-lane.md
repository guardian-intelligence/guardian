# 0010 — Two release signatures, one format per lane

Status: Accepted · Date: 2026-07-12

## Context

Guardian publishes container images to public registries. Marketplace
ecosystems (npm provenance, PyPI Trusted Publishers, GitHub artifact
attestations) accept only CI-platform OIDC identities — a bring-your-own
key is not currency there. A GitHub-anchored identity, however, is exactly
the dependency the registry-sovereignty work sheds everywhere else: Guardian
needs a signature it owns outright, verifiable if GitHub, Sigstore's TUF
roots, or any third party is gone.

cosign stores image signatures in two layouts: the original tag-based one
("legacy", `sha256-<digest>.sig` — readable by every cosign since v1, by
policy controllers and scanners, and by registries with no OCI referrers
support) and the bundle layout (an OCI 1.1 referrer embedding certificate
and Rekor inclusion proof). cosign v3's default verification inspects only
bundles once any bundle referrer is attached to a digest; it never falls
back to tags. The zot mirror adds a hard constraint: a tag GET re-triggers
on-demand sync, which clobbers locally written tags, while referrers
survive sync — signatures born in-cluster cannot live in tags.

## Decision

Every released first-party digest carries two signatures, each in the
format of its origin:

- **CI signs keyless** (GitHub Actions + Fulcio) in the **legacy tag
  layout**: born on ghcr, mirrored inward, aimed at the broadest possible
  verifier base — lowest-common-denominator readability is that lane's job.
- **The countersigner mints Guardian's release signature** with the
  Transit-held, non-exportable `guardian-images` key (recovered through
  raft-snapshot DR, never held in custody) in the **bundle layout**, Rekor-logged
  with the inclusion proof embedded: born in zot, where tags are unsafe,
  and verifiable with the committed public key alone — stock cosign
  defaults online, or fully offline with the pinned trusted root.

The CI lane is not migrated to the bundle layout. Released digests keep
their tag signatures forever, so a migration would force every verifier to
read both lanes for its entire transition, and it trades away readability
exactly where readability is the point (GitHub's own registry currently
serves bundle referrers only via the fallback tag). Revisit when default
ecosystem tooling — registry referrers APIs, policy controllers, scanners —
has tipped to bundles.

## Consequences

- Guardian's signature verifies with stock defaults (`cosign verify
  --key`). Verifying the CI signature on any countersigned digest requires
  `--new-bundle-format=false`: bundle-first discovery shadows the tag lane
  the moment the countersignature lands. This bit live once — the
  countersigner's own Fulcio re-verify failed estate-wide until pinned to
  the legacy lane — and any new Fulcio-verifying consumer must carry the
  flag.
- Nothing Guardian releases ships without the countersignature, enforced
  at the publication boundary by the release projector — never at
  admission or runtime.
- Either anchor can fail without taking the other: a GitHub or Sigstore
  outage leaves the Guardian lane verifiable; loss of the Guardian key
  costs a re-key and re-sign, never data.
- Living detail: `docs/supply-chain-design.md` (trust model, who signs
  what, Fulcio identities), `docs/registry-design.md` (countersigner,
  release projector, zot sync constraints).

Related source: `src/infrastructure/deployments/guardian/system/zot-countersigner.yaml`,
`src/infrastructure/deployments/guardian/system/release-projector.yaml`,
`src/infrastructure/bootstrap/bundle/guardian-images.pub.pem`
