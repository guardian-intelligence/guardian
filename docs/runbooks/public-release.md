# Public artifact release

This is the public vending lane that runs after the fleet release gate.
`.github/workflows/release.yml` still deploys gamma/prod on the self-hosted
runner and cuts `aisucks/v<N>` only after prod is green. The tag push triggers
`.github/workflows/public-release.yml` on a GitHub-hosted runner, which holds
no cluster credentials.

## What Ships

- OCI image: `ghcr.io/guardian-intelligence/aisucks@sha256:<digest>`
- OCI tags: `v<N>` and `git-<12-char-sha>`
- Cosign keyless signature over the digest
- Cosign in-toto/SLSA provenance attestation over the digest

The OCI digest still comes from `bazelisk build //:build`; the public workflow
pushes the already-built layout with:

```sh
bazelisk run //src/products/aisucks/services/api:publish_ghcr -- --tag v<N>
```

`rules_oci` pushes the image by digest first, then applies tags.

## Required Repository Setup

GitHub:

- `public-release.yml` must stay on a GitHub-hosted runner. Cosign keyless
  signing depends on GitHub OIDC.
- The workflow needs `contents: read`, `packages: write`, and
  `id-token: write`.

GHCR:

- The workflow uses `GITHUB_TOKEN` to push `ghcr.io/guardian-intelligence/aisucks`.
- The package should be made public in the GitHub Packages UI after the first
  publish if GitHub defaults it to private.

The npm SDK release lane is intentionally separate and package-scoped. See
`docs/runbooks/npm-sdk-release.md`.

## Verify OCI Signature

Use the digest printed by the workflow:

```sh
IMAGE='ghcr.io/guardian-intelligence/aisucks@sha256:<digest>'

cosign verify "$IMAGE" \
  --certificate-identity-regexp '^https://github.com/guardian-intelligence/guardian/.github/workflows/public-release.yml@refs/tags/aisucks/v[0-9]+$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

Expected: cosign reports the certificate identity checks and emits the verified
signature payload.

## Verify OCI Provenance Attestation

```sh
IMAGE='ghcr.io/guardian-intelligence/aisucks@sha256:<digest>'

cosign verify-attestation "$IMAGE" \
  --type slsaprovenance \
  --certificate-identity-regexp '^https://github.com/guardian-intelligence/guardian/.github/workflows/public-release.yml@refs/tags/aisucks/v[0-9]+$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  | jq -r '.payload' \
  | base64 -d \
  | jq .
```

Expected predicate facts:

- `predicateType` is SLSA provenance.
- `predicate.invocation.parameters.repository` is
  `https://github.com/guardian-intelligence/guardian`.
- `predicate.invocation.parameters.bazelTarget` is
  `//src/products/aisucks/services/api:publish_ghcr`.
- `predicate.builder.id` names `public-release.yml` at the release
  tag ref.

## Later Fleet Admission

Do not enforce this in-cluster yet. The eventual admission rule is:

- Image digest is the exact digest selected by the release pointer.
- Cosign certificate identity matches `public-release.yml` on a release tag.
- OIDC issuer is `https://token.actions.githubusercontent.com`.
- SLSA provenance subject digest matches the image digest.
- A fleet-signed `gate-pass` attestation exists for that digest.
- No `rejected`/taint attestation exists for that digest.

That belongs in the future release judge / admission-controller slice, not in
today's bootstrap renderer.
