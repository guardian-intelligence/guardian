# npm SDK release

The aisucks SDK release is package-scoped. GitHub Actions is only the runner;
the release decision lives in repo tooling:

```sh
aspect release npm-sdk
```

CI runs the same command in publish mode after installing Guardian's pinned tool
shims. The Aspect task delegates to `scripts/release/npm-aisucks-sdk.sh`, which
reads `src/viteplus-monorepo/packages/aisucks-sdk`, checks whether
`@guardian-intelligence/aisucks@<package.json version>` already exists on npm
with the same tarball integrity, and exits 0 when it does. It never decides
from GitHub Actions `paths`.

## Release intent

Use Changesets for user-facing SDK changes:

```sh
cd src/viteplus-monorepo
vp run -w changeset
vp run -w changeset:version
```

Review the generated `CHANGELOG.md` and `package.json` version bump. The
package is releasable only after SDK-specific `.changeset/*.md` files have
been applied by the version step; the release task refuses to hide pending SDK
release intent behind a no-op.

## Local probe

From the repo root:

```sh
aspect release npm-sdk
```

Expected outcomes:

- If the exact package version already exists on npm and the locally packed
  tarball integrity matches, the task prints a no-op and exits 0.
- If the package version exists but HEAD packs different bytes, the task fails:
  apply an SDK Changeset so npm receives a new external version, or restore the
  package bytes.
- If the version is new, the task builds
  `//src/viteplus-monorepo:workspace_build`, runs `npm pack`, and prints that
  the package is publishable.

## CI publish

`.github/workflows/npm-sdk-release.yml` runs on every merge to main and on
manual dispatch. It has no path filter. The publish step is:

```sh
aspect release npm-sdk --publish
```

The script publishes only when the package version is missing from npm. A
service-only change under the same SDK version therefore packs the same SDK
tarball and no-ops for npm while the service release lane can still publish its
own artifact.

Required GitHub setup:

- Workflow runs on a GitHub-hosted runner.
- Permissions: `contents: read`, `id-token: write`.
- Environment: `npm-release`.
- npm Trusted Publishing is configured for this workflow/environment.

Trusted Publishing configuration:

- Provider: GitHub Actions
- Organization/user: `guardian-intelligence`
- Repository: `guardian`
- Workflow filename: `npm-sdk-release.yml`
- Environment: `npm-release`
- Allowed action: `npm publish`

## Verify package

```sh
PKG='@guardian-intelligence/aisucks@<version>'

npm view "$PKG" \
  --registry=https://registry.npmjs.org/ \
  name version dist.integrity repository.url
```

Expected:

- `name` is `@guardian-intelligence/aisucks`.
- `repository.url` is `git+https://github.com/guardian-intelligence/guardian.git`.
- npmjs.com shows the provenance badge for versions published with
  `npm publish --provenance`.

The Sigstore search UI is a convenience surface only. If
`https://search.sigstore.dev/?logIndex=<index>` reports a browser
`NetworkError`, verify npm attestations directly:

```sh
npm view "$PKG" \
  --registry=https://registry.npmjs.org/ \
  dist.attestations.url dist.attestations.provenance.predicateType

curl -fsS "https://registry.npmjs.org/-/npm/v1/attestations/%40guardian-intelligence%2Faisucks@<version>" \
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
