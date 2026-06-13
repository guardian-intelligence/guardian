# Public artifact release

Status: target runbook. The old workflow-owned public release bridge has been
removed. Public vending should be reintroduced as repo-owned Go release tooling
executed through `aspect`, with any GitHub YAML reduced to an executor shim.

This lane runs after the fleet release gate. It should publish immutable
artifacts, signatures, attestations, and release metadata for selected release
targets. The release tool owns the target tuple and all publish/no-op logic.
Reference naming is defined in `docs/architecture/oci-artifact-references.md`.

## What Ships

- API OCI image: `ghcr.io/guardian-intelligence/aisucks@sha256:<digest>`
- SDK OCI artifact:
  `oci.gi.org/guardian/aisucks/sdk/npm@sha256:<manifest>`
- OCI tags: `edge`, `nightly`, `stable`, `v<N>`, and `git-<12-char-sha>`
- Cosign keyless signature over the digest
- Cosign in-toto/SLSA provenance attestation over the digest

The OCI digest still comes from `bazelisk build //:build`; the release tool
pushes the already-built layout with:

```sh
bazelisk run //src/products/aisucks/services/api:publish_ghcr -- --tag v<N>
```

`rules_oci` pushes the image by digest first, then applies tags.

The SDK OCI subject is built through Aspect:

```sh
aspect release sdk-oci
oras pull --oci-layout dist/release/aisucks-sdk-oci:edge -o ./dist
```

When the public registry exists and write credentials are present, the remote
publish form is:

```sh
aspect release sdk-oci \
  --publish \
  --ref oci.gi.org/guardian/aisucks/sdk/npm:edge \
  --username guardian-release \
  --password-env GUARDIAN_OCI_PASSWORD
```

## Required Setup

Executor:

- Any hosted executor used for cosign keyless signing needs GitHub OIDC
  permissions or an equivalent configured identity provider.
- Workflow files must not encode release matrices, publisher fan-out, signing,
  attestation, verification, or no-op decisions. Those belong in the release
  binary invoked by `aspect`.

GHCR:

- The release tool needs authority to push `ghcr.io/guardian-intelligence/aisucks`.
- The package should be made public in the GitHub Packages UI after the first
  publish if GitHub defaults it to private.

The npm SDK release lane is intentionally a downstream projection from the SDK
OCI artifact. See `docs/runbooks/npm-sdk-release.md`.

## Verify OCI Signature

Use the digest printed by the release tool:

```sh
IMAGE='ghcr.io/guardian-intelligence/aisucks@sha256:<digest>'

cosign verify "$IMAGE" \
  --certificate-identity-regexp '<expected release builder identity>' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

Expected: cosign reports the certificate identity checks and emits the verified
signature payload. Historical bridge releases used the deleted public release
workflow identity; future releases should pin the replacement release-builder
identity recorded in provenance.

## Verify OCI Provenance Attestation

```sh
IMAGE='ghcr.io/guardian-intelligence/aisucks@sha256:<digest>'

cosign verify-attestation "$IMAGE" \
  --type slsaprovenance \
  --certificate-identity-regexp '<expected release builder identity>' \
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
