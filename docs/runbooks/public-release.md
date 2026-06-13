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
- npm package: `@guardian-intelligence/aisucks@0.1.<N>` when npm publishing is
  enabled

The OCI digest still comes from `bazelisk build //:build`; the public workflow
pushes the already-built layout with:

```sh
bazelisk run //src/products/aisucks/services/api:publish_ghcr -- --tag v<N>
```

`rules_oci` pushes the image by digest first, then applies tags.

## Required Repository Setup

GitHub:

- `public-release.yml` must stay on a GitHub-hosted runner. npm trusted
  publishing and cosign keyless signing both depend on GitHub OIDC.
- The workflow needs `contents: read`, `packages: write`, and
  `id-token: write`.
- Environment: `npm-release`.
- Variable: `NPM_PUBLISH_ENABLED=true` enables npm publishing. Leave it unset
  until the npm package exists and either trusted publishing or the temporary
  bootstrap token is ready.

GHCR:

- The workflow uses `GITHUB_TOKEN` to push `ghcr.io/guardian-intelligence/aisucks`.
- The package should be made public in the GitHub Packages UI after the first
  publish if GitHub defaults it to private.

npm:

- Org: `guardian-intelligence`.
- Package: `@guardian-intelligence/aisucks`.
- Trusted publishing cannot be configured until the package already exists on
  npm. That is npm's current rule for `npm trust`.

First publish options:

1. Preferred bootstrap: create a temporary granular npm token scoped to
   `@guardian-intelligence/aisucks`, store it as the `NPM_TOKEN` secret on the
   `npm-release` environment, set `NPM_PUBLISH_ENABLED=true`, run one tagged
   release with `npm publish --access public --provenance`, then revoke the
   token after trusted publishing is configured.
2. Manual fallback: publish once interactively from the package directory with
   `npm publish --access public`, then configure trusted publishing. This does
   not produce GitHub Actions provenance for that first version unless published
   from a trusted CI environment with `--provenance`.

After the package exists, configure npm trusted publishing:

- Provider: GitHub Actions
- Organization/user: `guardian-intelligence`
- Repository: `guardian`
- Workflow filename: `public-release.yml`
- Environment: `npm-release`
- Allowed action: `npm publish`

Then remove `NPM_TOKEN`, keep `NPM_PUBLISH_ENABLED=true`, and set the npm
package's publishing access to disallow token publishing if desired. Trusted
publishing generates npm provenance automatically for public packages from a
public GitHub repository.

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

## Verify npm Package

After npm publishing is enabled:

```sh
npm view @guardian-intelligence/aisucks@0.1.<N> \
  --registry=https://registry.npmjs.org/ \
  name version dist.integrity repository.url
```

Expected:

- `name` is `@guardian-intelligence/aisucks`.
- `version` is `0.1.<N>`.
- `repository.url` is `git+https://github.com/guardian-intelligence/guardian.git`.
- npmjs.com shows the provenance badge for versions published through trusted
  publishing or `npm publish --provenance`.

The Sigstore search UI is a convenience surface only. If
`https://search.sigstore.dev/?logIndex=<index>` reports a browser
`NetworkError`, verify the npm attestations directly:

```sh
PKG='@guardian-intelligence/aisucks@0.1.<N>'

npm view "$PKG" \
  --registry=https://registry.npmjs.org/ \
  dist.attestations.url dist.attestations.provenance.predicateType

curl -fsS "https://registry.npmjs.org/-/npm/v1/attestations/%40guardian-intelligence%2Faisucks@0.1.<N>" \
  | jq -r '.attestations[] | [.predicateType, .bundle.verificationMaterial.tlogEntries[0].logIndex] | @tsv'
```

Expected:

- One attestation is npm publish metadata:
  `https://github.com/npm/attestation/tree/main/specs/publish/v0.1`.
- One attestation is SLSA provenance: `https://slsa.dev/provenance/v1`.
- The SLSA attestation's `logIndex` is retrievable from Rekor:

```sh
curl -fsS "https://rekor.sigstore.dev/api/v1/log/entries?logIndex=<logIndex>" \
  | jq .
```

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
