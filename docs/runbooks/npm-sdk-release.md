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

The SDK artifact lane is package-owned. The durable command surface is Aspect,
but the release policy and state machine live in
`src/viteplus-monorepo/packages/aisucks-sdk/release/`.

Local check mode builds the package through Bazel, creates a local OCI layout,
runs admission, and stops before public writes:

```sh
aspect release sdk-oci
```

Publish mode requires a trusted executor with OIDC and registry write
credentials:

```sh
aspect release sdk-oci \
  --publish \
  --channel edge \
  --ref oci.guardianintelligence.org/guardian/aisucks/sdk/npm:edge
```

The package release script builds these inputs through Bazel:

- `//src/viteplus-monorepo:vp_node`
- `//src/viteplus-monorepo/packages/aisucks-sdk:npm_package`
- `//src/release/cmd/sdkoci`

It writes `release-result.json` in the selected output directory. Check mode
defaults to a temporary directory; pass `--output-dir <dir>` to keep the local
OCI layout and release records.

`sdkoci` is the low-level OCI pack/push helper. It does not decide whether a
release should happen; it only writes the package tarball payload as an OCI
artifact. The release state machine signs and attests the pushed subject with
stock cosign commands.

This milestone must produce:

- npm package tarball built from repo source
- OCI subject at `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>`
- tarball digest and npm `dist.integrity`
- `synthetic-result.v1` and `gate-result.v1` JSON for edge→nightly and
  nightly→RC Health gates
- npm dist-tags for the selected channel after the gate passes

Public release and promotion runs request an in-toto/SLSA provenance statement,
cosign v3 keyless OCI signing, cosign v3 keyless OCI attestation, and npm
Trusted Publishing provenance. Local check runs may omit those flags because
they do not have GitHub OIDC identity or public registry write authority.
Public verification uses stock cosign commands.

The OCI reference forms are defined in
`docs/architecture/oci-artifact-references.md`.

## npm Projection

The npm publisher takes an already selected OCI subject and performs only the
npm-specific projection:

```text
verify OCI subject
  -> pull guardian-intelligence-aisucks-<version>.tgz
  -> confirm package name/version/integrity
  -> npm publish ./guardian-intelligence-aisucks-<version>.tgz --tag <tag>
  -> ensure the npm dist-tag points at the verified version
```

Expected no-op behavior:

- If npm already has the exact package version and the tarball integrity
  matches the OCI subject, projection exits 0 after ensuring the dist-tag.
- If npm already has the version but the tarball bytes differ, projection
  fails. Apply an SDK Changeset so npm receives a new external version, or
  restore the package bytes.
- If npm is missing the version, projection publishes through the GitHub
  executor shim.

## Executor Requirements

npm Trusted Publishing currently requires a GitHub-hosted Actions runner with
OIDC. The executor shim is `.github/workflows/npm-sdk-release.yml`.
Required setup:

- Release job permissions: `contents: read`, `id-token: write`.
- npm Trusted Publishing is configured for the exact workflow filename and
  repository.
- Public signing and attestation are only valid when `GITHUB_WORKFLOW_REF` is
  `guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml@refs/heads/main`.
  Dispatch the workflow from `main`; use `source-ref` only when deliberately
  releasing a selected source ref under the main workflow identity.
- `package.json` `repository.url` is the exact GitHub repository URL:
  `https://github.com/guardian-intelligence/guardian`.
- Publish preflight requests a GitHub OIDC token for
  `npm:registry.npmjs.org` and verifies npm accepts it for the package before
  public writes.
- The workflow runs one `aspect release sdk-oci --publish ...` task.
- The workflow YAML must not encode release policy, package matrices,
  publisher fan-out, signing, attestation, verification, or no-op decisions.
- `GUARDIAN_OCI_PASSWORD` gives the release task zot write authority.
  `GUARDIAN_OCI_ACCESS_TOKEN` is rejected for signed SDK publication until
  cosign token-stdin support is wired.
- No `NPM_TOKEN` is used; npm issues publish authority from GitHub OIDC.

Trusted Publishing configuration:

- Provider: GitHub Actions
- Organization/user: `guardian-intelligence`
- Repository: `guardian`
- Workflow filename: `npm-sdk-release.yml`
- Allowed action: `npm publish`

## Health Gate

The same workflow can run the public SDK Health gate and upload exact metrics
as GitHub Actions artifacts:

```sh
aspect release sdk-gate \
  --track nightly \
  --from-channel edge \
  --to-channel nightly \
  --endpoint https://gamma.aisucks.app \
  --output-dir /tmp/guardian-aisucks-gate
```

The gate installs `@guardian-intelligence/aisucks@<from-channel>` from npm,
calls Connect Health through the installed SDK, and emits:

- `synthetic-result.v1.json`
- `gate-result.v1.json`
- `promotion-vsa.v1.json`
- `promotion-vsa.sigstore.bundle.json`
- `promotion-vsa.intoto.jsonl`
- `gate-summary.md`

The current checks are synthetic success, required Health capability, p95
latency, observed TPS, tarball bytes, and unpacked package bytes. A passing
gate can then run `aspect release sdk-oci --publish --channel <to-channel>
--with-attestation --sign-oci --npm-provenance ...` to move the OCI tag and npm
dist-tag through the same package-owned release logic. If npm already has the
exact package version and integrity, this is an idempotent dist-tag operation;
if the bytes differ, the release fails.

## Verify OCI Subject

Local layout verification:

```sh
aspect release sdk-oci --output-dir /tmp/guardian-sdk-release
oras pull --oci-layout /tmp/guardian-sdk-release/oci-layout:edge -o ./dist
oras discover --oci-layout /tmp/guardian-sdk-release/oci-layout:edge
jq . /tmp/guardian-sdk-release/release-result.json
```

Public registry verification once `oci.guardianintelligence.org` is live:

```sh
SDK='oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>'

oras pull "$SDK" -o ./dist
oras discover "$SDK"
cosign verify "$SDK" \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'

cosign verify-attestation "$SDK" \
  --type slsaprovenance1 \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

Expected:

- `oras pull` writes exactly one npm `.tgz` payload.
- `cosign verify` reports one verified keyless signature for the release
  workflow identity above.
- `cosign verify-attestation` verifies one DSSE envelope whose payload is an
  in-toto Statement with SLSA provenance predicate.

## Declare Release Subject

Release declaration is the first admission boundary for an external version. It
resolves the SDK version tag to an immutable OCI digest, verifies the cosign v3
signature and SLSA provenance against the pinned main workflow identity, checks
that the provenance names the declared source commit, and only then writes
`release-declaration.json`.

```sh
aspect release declare \
  --product aisucks \
  --version 1.2.3-rc.1 \
  --commit <40-char-source-sha> \
  --track rc
```

The default OCI input is
`oci.guardianintelligence.org/guardian/aisucks/sdk/npm:npm-v<version>`. Use
`--oci-ref <ref>` only when deliberately declaring a different already-published
SDK subject. Admission fails before writing the declaration if `oras resolve`,
`cosign verify`, `cosign verify-attestation`, the SLSA subject digest, the
release target version/package, the source commit, or the builder id do not
match the contract.

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
