# OCI Artifact References

Status: target convention. Examples use the deployed public registry hostname
`oci.guardianintelligence.org`.

Guardian's public artifact surface is the OCI Distribution API. Object storage
is an implementation detail behind the registry. Clients pull OCI references
and verify OCI subjects; they do not pull from S3/R2 buckets directly.

## Naming Rule

The repository path is the collapsed release tuple for the bytes being served:

```text
<registry>/<namespace>/<product>/<distributable>/<payload-form>[/<platform>][/<flavor>][:tag|@sha256:<manifest-digest>]
```

For the aisucks TypeScript SDK:

```text
oci.guardianintelligence.org/guardian/aisucks/sdk/npm
```

Tuple fields:

- `namespace`: `guardian`
- `product`: `aisucks`
- `distributable`: `sdk`
- `payload-form`: `npm`
- `platform`: `any`, omitted because the initial SDK tarball is platform
  neutral
- `flavor`: `default`, omitted until a non-default flavor exists

The path names the payload form, not the publisher. `npm` is present because
the payload is an npm package tarball. `npmjs-public` is not present because it
is a publication target for the same tarball bytes.

## Pullable SDK Forms

OCI references are not directories. A pullable reference uses either a tag or a
digest:

| Use | Reference | Trust rule |
| --- | --- | --- |
| Canonical subject | `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>` | Verification target. |
| Fast candidate | `oci.guardianintelligence.org/guardian/aisucks/sdk/npm:edge` | Resolve to digest, then verify. |
| Gated candidate | `oci.guardianintelligence.org/guardian/aisucks/sdk/npm:nightly` | Resolve to digest; require gate result. |
| Stable user channel | `oci.guardianintelligence.org/guardian/aisucks/sdk/npm:stable` | Resolve to digest; require stable pointer/provenance. |
| Commit convenience | `oci.guardianintelligence.org/guardian/aisucks/sdk/npm:git-<12-char-sha>` | Debugging/rebuild correlation only. |
| Ecosystem version | `oci.guardianintelligence.org/guardian/aisucks/sdk/npm:npm-v0.3.0` | Ties the OCI subject to the npm external coordinate. |

Tags are convenience selectors. They are not the trust anchor. The digest,
signature, provenance, release manifest, and gate/publish referrers are the
trust anchor.

## SDK Artifact Envelope

The npm SDK subject is an OCI manifest used as a generic artifact, not a
runnable container image. The initial payload is deliberately boring:

```text
OCI subject: oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>
artifactType: application/vnd.guardian.sdk.npm.package.v1

layers:
  - title: guardian-intelligence-aisucks-<version>.tgz
    mediaType: application/gzip
    digest: sha256:<tarball-bytes>
```

The npm tarball is not unpacked, rewritten, or normalized by OCI. `oras pull`
writes the `.tgz` layer to disk. The npm publisher later uploads those same
bytes to npm:

```text
Bazel package build
  -> npm .tgz
  -> OCI artifact subject
  -> DSSE / in-toto / release metadata as referrers
  -> npm publisher pulls and verifies the OCI subject
  -> npm publish ./guardian-intelligence-aisucks-<version>.tgz --tag <tag>
```

## Referrers

Every release fact that is about the SDK artifact attaches to the OCI subject
digest as a referrer:

- cosign signature
- DSSE envelope over an in-toto Statement with SLSA provenance predicate
- SBOM
- release manifest
- gate result
- npm publish result

Publication results are referrers, not alternate source artifacts. If the same
SDK tarball is published to npmjs and a future internal npm registry, the OCI
subject stays the same and receives two publish-result referrers.

If a publisher requires different bytes, that is a different payload form and
therefore a different OCI subject.

## Platform And Flavor

The initial SDK is pure TypeScript, so the release tuple records
`platform=any` and `flavor=default`; both are omitted from the path.

If the bytes become platform-specific, add the platform segment:

```text
oci.guardianintelligence.org/guardian/aisucks/sdk/npm/linux-amd64:edge
oci.guardianintelligence.org/guardian/aisucks/sdk/npm/darwin-arm64:edge
```

If the bytes vary by build flavor, add the flavor after platform:

```text
oci.guardianintelligence.org/guardian/aisucks/sdk/npm/linux-amd64/fips:edge
```

Do not add publisher, channel, source commit, or version as path segments.
Those are tags, release-manifest fields, annotations, or referrer payloads.

## Verification Shape

The package-owned release check creates the same artifact envelope without a
registry:

```sh
aspect release sdk-oci --output-dir /tmp/guardian-sdk-release
guardian run oras pull --oci-layout /tmp/guardian-sdk-release/oci-layout:edge -o ./dist
guardian run oras discover --oci-layout /tmp/guardian-sdk-release/oci-layout:edge
```

The release state machine writes `/tmp/guardian-sdk-release/release-result.json`
with the OCI manifest digest, tarball sha256, npm integrity, package version,
source commit, DSSE/in-toto evidence paths, and event log.

When Bazel is invoked with `--embed_label=<40-char-git-sha>`, that commit is
recorded in the OCI annotations and result JSON. Unstamped local builds use a
deterministic zero commit placeholder so repeated builds produce the same
manifest digest.

A clean machine should eventually verify the public SDK by digest:

```sh
SDK='oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>'

guardian run oras pull "$SDK" -o ./dist
guardian run oras discover "$SDK"
guardian run cosign verify "$SDK" \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
npm install ./dist/guardian-intelligence-aisucks-<version>.tgz
```

For the SDK lane, publish mode emits both the DSSE/in-toto JSONL evidence
referrer and a cosign keyless signature over the OCI subject. npm Trusted
Publishing provenance is still a downstream ecosystem receipt for the npmjs
projection, not the OCI subject's trust anchor.

A blackbox canary that starts from a channel should resolve the tag to a
digest, verify the digest, pull the tarball, install it, call Connect Health,
and emit a synthetic result tied back to the resolved OCI subject digest.
