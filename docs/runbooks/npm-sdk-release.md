# npm SDK Projection

Status: target runbook. npm is not the source artifact store for Guardian
releases. The canonical SDK candidate is the npm package tarball stored as an
OCI artifact at:

```text
oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>
```

npm publication is a downstream projection from that verified OCI subject. The
GitHub-hosted OIDC requirement for npm Trusted Publishing is real, but GitHub
Actions remains an executor shim only. It must not own release selection,
publisher fan-out, no-op policy, SLO gates, or channel promotion.
In OCI paths, `npm` names the npm package tarball format; it does not mean
npmjs.com is the artifact's source of truth.

## Release Intent

Use Changesets for user-facing SDK changes:

```sh
cd src/viteplus-monorepo
vp run -w changeset
vp run -w changeset:version
```

Review the generated `CHANGELOG.md` and `package.json` version bump. The
package is releasable only after SDK-specific `.changeset/*.md` files have
been applied by the version step; package-owned static checks refuse to hide
pending SDK release intent behind a release no-op:

```sh
cd src/viteplus-monorepo
vp run -w lint
```

This runs VitePlus linting plus the TypeScript workspace release hygiene check.
The check uses `@changesets/read` to parse pending Changesets and
`@manypkg/get-packages` to discover publishable workspace packages. The
top-level Bazel build reaches it through
`//src/viteplus-monorepo:workspace_lint`, so repo-level build orchestration
does not need to know Changesets semantics.

## Canonical Artifact

The SDK artifact lane starts locally and uses the same envelope that the public
registry will vend:

```sh
bazelisk build //src/viteplus-monorepo/packages/aisucks-sdk:sdk_oci
oras pull --oci-layout bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/sdk_oci.oci:edge -o ./dist
```

The Bazel target builds `//src/viteplus-monorepo/packages/aisucks-sdk:npm_package`
and runs the repo-owned Go artifact builder at `//src/release/cmd/sdkoci`. It
writes a machine-readable result:

```sh
jq . bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/sdk_oci.json
```

For release-grade commit annotations, build with Bazel's embed label set to
the source commit. Without an embed label, the local artifact remains
deterministic and records the zero commit placeholder:

```sh
bazelisk build --embed_label=<40-char-git-sha> //src/viteplus-monorepo/packages/aisucks-sdk:sdk_oci
```

Remote publication is explicit:

```sh
aspect release sdk-oci \
  --publish \
  --ref oci.guardianintelligence.org/guardian/aisucks/sdk/npm:edge \
  --username guardian-release \
  --password-env GUARDIAN_OCI_PASSWORD
```

The SDK artifact lane must produce:

- npm package tarball built from repo source
- OCI subject at `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>`
- tarball digest and npm `dist.integrity`
- SLSA/in-toto provenance naming the source commit, Bazel target, builder
  identity, and release tuple
- release manifest entry linking the OCI subject to the npm package coordinate

The OCI reference forms are defined in
`docs/architecture/oci-artifact-references.md`.

## npm Projection

The npm publisher takes an already selected OCI subject and performs only the
npm-specific projection:

```text
verify OCI subject
  -> pull guardian-intelligence-aisucks-<version>.tgz
  -> confirm package name/version/integrity
  -> npm publish ./guardian-intelligence-aisucks-<version>.tgz --tag <tag> --provenance
  -> attach publish-result referrer to the OCI subject
```

Expected no-op behavior:

- If npm already has the exact package version and the tarball integrity
  matches the OCI subject, projection exits 0.
- If npm already has the version but the tarball bytes differ, projection
  fails. Apply an SDK Changeset so npm receives a new external version, or
  restore the package bytes.
- If npm is missing the version, projection publishes with npm Trusted
  Publishing provenance.

## Executor Requirements

npm Trusted Publishing currently requires a GitHub-hosted Actions runner with
OIDC. When the executor shim is reintroduced, required setup is:

- Workflow is manually invoked or called by the repo-owned release controller;
  it does not run on every merge to main.
- Permissions: `contents: read`, `id-token: write`.
- Environment: `npm-release`.
- npm Trusted Publishing is configured for the exact workflow filename and
  environment.
- The workflow runs a single `aspect` task that receives the selected OCI
  digest and npm tag. The workflow YAML must not encode release policy.
- OCI registry write credentials are explicit task inputs, such as
  `--username guardian-release --password-env GUARDIAN_OCI_PASSWORD` or
  `--access-token-env GUARDIAN_OCI_ACCESS_TOKEN`; the release task does not
  depend on host Docker credential-helper state.

Trusted Publishing configuration:

- Provider: GitHub Actions
- Organization/user: `guardian-intelligence`
- Repository: `guardian`
- Workflow filename: to be defined by the projection shim
- Environment: `npm-release`
- Allowed action: `npm publish`

## Verify OCI Subject

Local layout verification:

```sh
bazelisk build //src/viteplus-monorepo/packages/aisucks-sdk:sdk_oci
oras pull --oci-layout bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/sdk_oci.oci:edge -o ./dist
sha256sum ./dist/guardian-intelligence-aisucks-<version>.tgz
jq -r '.tarball_sha256' bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/sdk_oci.json
```

Public registry verification once `oci.guardianintelligence.org` is live:

```sh
SDK='oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>'

oras pull "$SDK" -o ./dist
cosign verify "$SDK"
cosign verify-attestation --type slsaprovenance "$SDK"
```

Expected:

- `oras pull` writes exactly one npm `.tgz` payload.
- cosign verifies the release-builder identity.
- SLSA provenance subject digest matches the OCI subject digest.

## Verify npm Projection

```sh
PKG='@guardian-intelligence/aisucks@<version>'

npm view "$PKG" \
  --registry=https://registry.npmjs.org/ \
  name version dist.integrity repository.url
```

Expected:

- `name` is `@guardian-intelligence/aisucks`.
- `repository.url` is
  `git+https://github.com/guardian-intelligence/guardian.git`.
- `dist.integrity` matches the integrity recorded in the release manifest.
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
