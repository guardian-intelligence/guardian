# Public artifact release

Status: current runbook for the SDK lane, target runbook for later service
images. The old workflow-owned public release bridge has been removed. Public
vending is reintroduced as package-owned release tooling executed through
`aspect`, with any GitHub YAML reduced to an executor shim.

This lane runs after the fleet release gate. It should publish immutable
artifacts, signatures, attestations, and release metadata for selected release
targets. The release tool owns the target tuple and all publish/no-op logic.
Reference naming is defined in `docs/architecture/oci-artifact-references.md`.
The public verification contract is stock cosign v3: blob signatures ship as
Sigstore bundle sidecars, and OCI signatures/attestations are discovered as
standard OCI 1.1 referrers.

The split is deliberate:

- Build produces local bytes and a digest. It has no public side effects.
- Publish copies that digest to `oci.guardianintelligence.org` and attaches
  evidence as OCI referrers.
- Distribute resolves, pulls, and verifies the digest, or projects the verified
  subject into provider-specific systems such as npm, crates.io, PyPI, or Zig.

## What Ships

- CLI GitHub Release asset:
  `guardian_<version>_linux_amd64.tar.gz`
- CLI blob signature bundle:
  `guardian_<version>_linux_amd64.tar.gz.sigstore.json`
- CLI DSSE/in-toto SLSA attestation bundle:
  `guardian_<version>_linux_amd64.tar.gz.intoto.sigstore.json`
- API OCI image:
  `oci.guardianintelligence.org/guardian/aisucks/api@sha256:<digest>`
- SDK OCI artifact:
  `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>`
- OCI tags: `edge`, `nightly`, `stable`, `v<N>`, and `git-<12-char-sha>`
- Cosign keyless signatures over each exact blob or OCI subject digest
- DSSE/in-toto SLSA provenance attestations over each exact blob or OCI
  subject digest
- npm Trusted Publishing provenance for the npm projection

The legacy `//src/products/aisucks/services/api:publish_ghcr` helper is not
the v0.4.0 public API lane. The target path is a package-owned release command
executed through `aspect`, which pushes
`oci.guardianintelligence.org/guardian/aisucks/api@sha256:<digest>`, signs the
digest, attaches DSSE/in-toto provenance, and then applies convenience tags.

The SDK OCI subject is built and admitted through Aspect:

```sh
aspect release sdk-oci --output-dir /tmp/guardian-sdk-release
oras pull --oci-layout /tmp/guardian-sdk-release/oci-layout:edge -o ./dist
oras discover --oci-layout /tmp/guardian-sdk-release/oci-layout:edge
```

The public zot registry allows anonymous reads and requires the
`guardian-release` htpasswd identity for writes. That credential is minted in
OpenBao at `kv/guardian/<site>/oci/zot-publisher` during secret-zero seeding;
the site's `SecretProjection` owns the namespace-scoped ESO projection to the
Kubernetes Secret `guardian-oci/zot-publisher`.

When write credentials are present, the remote publish form is:

```sh
aspect release sdk-oci \
  --publish \
  --channel edge \
  --ref oci.guardianintelligence.org/guardian/aisucks/sdk/npm:edge
```

`aspect release sdk-oci --publish` defaults to the ref above, the
`guardian-release` OCI registry username, and `GUARDIAN_OCI_PASSWORD`.
Publish mode signs the pushed SDK subject with cosign keyless signing, so it
currently requires `GUARDIAN_OCI_USERNAME`/`GUARDIAN_OCI_PASSWORD` basic auth.
Bearer-token OCI push works for unsigned pushes, but signed SDK publication
rejects `GUARDIAN_OCI_ACCESS_TOKEN` until cosign token-stdin support is wired.
npm publish authority comes from GitHub OIDC Trusted Publishing, not from
`NPM_TOKEN`.

## Required Setup

Executor:

- Any hosted executor used for cosign keyless signing needs GitHub OIDC
  permissions or an equivalent configured identity provider.
- Workflow files must not encode release matrices, publisher fan-out, signing,
  attestation, verification, or no-op decisions. Those belong in the release
  package invoked by `aspect`.

OCI registry:

- The release tool needs authority to push
  `oci.guardianintelligence.org/guardian/aisucks/api`.
- `oci.guardianintelligence.org` must allow anonymous digest reads and serve
  cosign v3 signatures and attestations as standard OCI 1.1 referrers.

The npm SDK release lane is intentionally a downstream projection from the SDK
OCI artifact. See `docs/runbooks/npm-sdk-release.md`.

## Verify CLI Release Asset

Use the exact version and asset name from the GitHub Release:

```sh
VERSION=v0.4.0
ASSET=guardian_${VERSION}_linux_amd64.tar.gz
BASE=https://github.com/guardian-intelligence/guardian/releases/download/${VERSION}

curl -fsSLO "$BASE/$ASSET"
curl -fsSLO "$BASE/$ASSET.sigstore.json"
curl -fsSLO "$BASE/$ASSET.intoto.sigstore.json"

cosign verify-blob "$ASSET" \
  --bundle "$ASSET.sigstore.json" \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/release.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'

cosign verify-blob-attestation "$ASSET" \
  --bundle "$ASSET.intoto.sigstore.json" \
  --type slsaprovenance1 \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/release.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

Expected: both commands verify the exact blob bytes, the signing certificate
identity is the `release.yml` workflow on `refs/heads/main`, and the
attestation payload is a DSSE envelope around an in-toto SLSA provenance
statement.

## Verify Current SDK OCI Evidence

Use the digest printed by the release tool:

```sh
SDK='oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<digest>'

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

Expected: `oras pull` writes the npm tarball payload. `cosign verify` reports
one verified keyless signature for the npm SDK release workflow identity.
`cosign verify-attestation` verifies the DSSE/in-toto SLSA provenance
attestation attached to the same OCI subject digest.

## Verify OCI Provenance Attestation

```sh
IMAGE='oci.guardianintelligence.org/guardian/aisucks/api@sha256:<digest>'

cosign verify "$IMAGE" \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/release.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'

cosign verify-attestation "$IMAGE" \
  --type slsaprovenance1 \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/release.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  | jq -r '.payload' \
  | base64 -d \
  | jq .
```

Expected predicate facts:

- `predicateType` is SLSA provenance.
- `predicate.invocation.parameters.repository` is
  `https://github.com/guardian-intelligence/guardian`.
- `predicate.invocation.parameters.bazelTarget` names the package-owned API
  OCI release target, not the legacy GHCR publish helper.
- `predicate.builder.id` names the repo-owned release builder and exact
  source ref.

## Later Fleet Admission

Do not enforce this in-cluster yet. The eventual admission rule is:

- Image digest is the exact digest selected by the release pointer.
- Cosign certificate identity matches the approved release-builder identity.
- OIDC issuer is `https://token.actions.githubusercontent.com`.
- SLSA provenance subject digest matches the image digest.
- A fleet-signed `gate-pass` attestation exists for that digest.
- No `rejected`/taint attestation exists for that digest.

That belongs in the future release judge / admission-controller slice, not in
today's bootstrap renderer.
