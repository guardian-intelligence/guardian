# Pipe to Remote Box distribution

Status: release architecture implemented; no package or release is published by
this change. The first public release remains gated on the external publisher
registrations in [External release gates](#external-release-gates).

This document defines how `pipe-to-remote-box` reaches users through GitHub
Releases, npm and crates.io without changing the bytes between verification and
publication. It complements `supply-chain-design.md` (repository trust model)
and `dependency-management.md` (pinned GitHub Actions and dependency policy).

## Public identity and targets

One identifier is used at every binary and release boundary:

- product: **Pipe to Remote Box**;
- binary, crate and GitHub asset prefix: `pipe-to-remote-box`;
- npm meta package: `@guardian-intelligence/pipe-to-remote-box`;
- OCI artifact: `ghcr.io/guardian-intelligence/pipe-to-remote-box`.

The four release targets are:

| Rust target | npm platform package |
| --- | --- |
| `x86_64-unknown-linux-musl` | `@guardian-intelligence/pipe-to-remote-box-linux-x64` |
| `aarch64-unknown-linux-musl` | `@guardian-intelligence/pipe-to-remote-box-linux-arm64` |
| `x86_64-apple-darwin` | `@guardian-intelligence/pipe-to-remote-box-darwin-x64` |
| `aarch64-apple-darwin` | `@guardian-intelligence/pipe-to-remote-box-darwin-arm64` |

The Rust toolchain and dependency graph are pinned in the product directory.
Release builds use locked dependencies, fixed cross-compilation inputs and
path remapping so build-machine paths do not enter the binaries. Commit and
builder provenance live in OCI annotations and Sigstore certificates rather
than changing otherwise identical binaries.

## Trust and signing identities

The GitHub repository, reviewed `main` history and the two post-merge workflow
identities are the public root of trust. Verifiers pin the GitHub Actions OIDC
issuer `https://token.actions.githubusercontent.com` and these complete
certificate identities:

```text
# Binaries, OCI artifact and SPDX attestation
https://github.com/guardian-intelligence/guardian/.github/workflows/pipe-to-remote-box-image.yml@refs/heads/main

# install.sh release asset
https://github.com/guardian-intelligence/guardian/.github/workflows/pipe-to-remote-box-release.yml@refs/heads/main
```

The image workflow builds all four targets from reviewed main, smoke-checks the
native output and object format, signs every binary with `cosign sign-blob`,
and writes the resulting `.sigstore.json` bundle beside it. It then publishes
one digest-addressed OCI artifact containing the four binaries, their bundles,
the canary fixture and notices, and the complete Apache-2.0 `LICENSE`. It binds
the license bytes with an OCI digest annotation, signs the artifact digest,
attaches an SPDX JSON SBOM attestation with `cosign attest --type spdxjson`, and
verifies the exact file allowlist, signature, attestation, license and every
blob bundle before completing. Keyless Fulcio signing means CI stores no
signing key.

The release workflow exports a pinned OCI artifact and verifies its signature,
SPDX attestation, annotations and all four binary bundles against the build
identity. It publishes those exact binaries and bundles; promotion never
rebuilds or re-signs them. The only newly signed release input is `install.sh`,
which is signed and immediately verified under the release-workflow identity.

Guardian's existing countersigner and release projector can add the independent
publication-boundary countersignature to the OCI digest. Neither component
changes the per-binary Sigstore bundles users verify.

## Channels and release contents

Tags are disjoint and machine-validated:

| Channel | Tag | GitHub prerelease |
| --- | --- | --- |
| nightly | `pipe-to-remote-box/nightly-YYYYMMDD[-HHMM]` | yes |
| rc | `pipe-to-remote-box/rc-YYYYMMDD[-HHMM]` | yes |
| stable | `pipe-to-remote-box/vX.Y.Z` | no |

Every channel pin moves together with the matching release-manifest entry,
preserving the countersigner/projector boundary. Kargo owns nightly: it may
advance that pair only after the exact edge digest has a fresh passing verdict
from the in-cluster OpenSSH deep test. Reviewed release changes own rc and
stable. Their reviewer must confirm the selected digest's qualification and
lineage; repository lockstep tests prevent either half of the pair from moving
alone.

A release contains:

- four `pipe-to-remote-box-<target>` binaries;
- the matching four `pipe-to-remote-box-<target>.sigstore.json` bundles;
- `pipe-to-remote-box-<version>-src.tar.gz`, cut from the build commit and
  containing the product license and dependency notices;
- `LICENSE` and `THIRD_PARTY_LICENSES.md` as checksum-covered detached assets;
- `install.sh` and `install.sh.sigstore.json`; and
- `checksums.txt`, generated last over every other release asset.

The SPDX JSON SBOM is an attestation on the OCI digest, not a detached GitHub
Release asset. That keeps the SBOM bound to the canonical multi-binary artifact
and verifiable through the same identity as the build.

## Verifying and installing a release

Never use the repository-wide `releases/latest` URL: this repository publishes
multiple products. Resolve a `pipe-to-remote-box/*` tag or select a stable
version explicitly.

The high-assurance installer path verifies the installer before giving it to a
shell:

```sh
version=1.2.3
tag_path="pipe-to-remote-box%2Fv$version"
base="https://github.com/guardian-intelligence/guardian/releases/download/$tag_path"

curl -fsSLO "$base/install.sh"
curl -fsSLO "$base/install.sh.sigstore.json"
cosign verify-blob \
  --bundle install.sh.sigstore.json \
  --certificate-identity https://github.com/guardian-intelligence/guardian/.github/workflows/pipe-to-remote-box-release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  install.sh
sh install.sh --version "$version"
```

`install.sh` supports stable (default), `--channel rc`, `--channel nightly`,
and an exact stable `--version X.Y.Z`. Its release parser accepts only the tag
grammar above and ignores unrelated repository releases, malformed tags and
tag-shaped release-note text. The script requires HTTPS/TLS 1.2 or newer,
checks the selected binary against the exact anchored entry in `checksums.txt`,
and then requires `cosign verify-blob` against the build identity. A valid
checksum alone is never enough.

The installer stages the verified executable inside the destination, runs
`pipe-to-remote-box --version`, and only then atomically gives it the final
name. A failed download, checksum, signature or smoke check leaves an existing
installation unchanged. Fresh implicit installs under `sudo` are refused;
`PIPE_TO_REMOTE_BOX_INSTALL_DIR` must make a shared destination deliberate.

Because the intended convenience form can be piped into `sh`, every statement
with effects lives inside `main` and the final call is a brace group. An
arbitrary proper prefix is therefore syntactically incomplete or contains only
definitions. `//src/products/pipe-to-remote-box/dist:installer_test` sweeps the
script body and every byte of its executable tail to keep truncation inert. The
same test proves exact tag filtering, checksum-before-signature ordering,
mandatory Sigstore verification and preservation of an existing binary on
failure.

## npm mirror

The npm lane runs only for a published, non-prerelease stable tag. It uses npm
trusted publishing over GitHub OIDC and carries no long-lived npm token. The
lane downloads all four GitHub Release binaries, verifies `checksums.txt` and
each build-time Sigstore bundle, and only then calls
`dist/npm/stage-packages.sh`.

The staging script injects the stable version, the complete Apache-2.0 license,
the required dependency notices and one verified binary into each OS/CPU
package. It stages the meta package with exact-version `optionalDependencies`.
All templates contain `0.0.0-dev`, so a release version has one source: its
validated stable tag.

The four platform packages publish before the meta package. Each publish uses
public access and npm provenance. Registry versions are immutable; a retry may
accept an already-published identical version but must fail on every other
error. The meta package contains no install or lifecycle script. Its Node shim
selects one optional platform dependency and executes the bundled native
binary.

## crates.io mirror

The crates.io lane also runs only for a published stable tag. Before executing
Cargo in its OIDC-authorized job, it extracts the exact OCI digest from the
Release body, verifies the build signature and SPDX attestation, and requires
the tag SHA to equal the artifact's signed main-branch revision annotation.
Only that authenticated source may install the pinned Rust toolchain, verify
Cargo metadata and the binary version, and run:

```sh
cargo publish --locked --package pipe-to-remote-box
```

Authentication comes from the crates.io trusted publisher through
`rust-lang/crates-io-auth-action`; there is no repository token secret. The
crate's explicit include set contains source, `Cargo.toml`, `Cargo.lock`,
`README.md`, the full `LICENSE` and `THIRD_PARTY_LICENSES.md`, so registry
consumers receive the same source and licensing inputs used for the GitHub
source archive.

crates.io is a source registry, so it does not redistribute the signed release
binary. Its provenance is the trusted-publisher identity plus the release tag;
users who require the exact signed bytes should use a GitHub Release asset, the
verified installer, or the npm native package whose lane re-verifies those
assets before publication.

## Promotion and public-distribution canaries

Two in-cluster rings are independent of the trusted publisher workflows:

1. **Digest-bound functional gate.** An hourly job resolves the public `edge`
   artifact by digest and verifies the main-only OCI signature, SPDX
   attestation, all four build-time blob signatures, artifact shape, object
   architectures, fixture, license and notice hashes, source revision, and the
   exact embedded probe hash. Only then does it expose the Linux binary and signed
   one-connection SSH fixture to an unprivileged journey container. The shipped
   binary invokes `/usr/bin/ssh`, performs public-key and strict host-key
   verification, transports its fixed probe, enforces directory boundaries,
   returns missing/non-directory errors, leaves the fixture unchanged, and
   terminates an established-but-hung session at its end-to-end deadline. The
   verdict metric is labeled with that immutable digest. Kargo's nightly Stage
   queries that exact label and will not move either channel file when the
   verdict is absent or failing.
2. **Public distribution canary.** Every six hours a manifest-driven job walks
   all nonempty channels through anonymous public endpoints. It binds the
   GitHub Release body and tag SHA back to the signed OCI revision, verifies the
   release asset set, checksums, Sigstore bundles, licenses, notices, object
   shapes and source archive, and then exercises the release binary and fresh
   installer through real OpenSSH. For stable it also verifies the exact npm
   native package and source shim plus the exact crates.io package checksum,
   named files and VCS lineage before execution. Per-channel and per-method
   metrics, repeated-failure alerts and a heartbeat/silence alert make failures
   visible without giving the canary GitHub write access or registry secrets.

The deep-test runner adjudicates the current `edge` digest once per hour. If an
edge tag is replaced before the runner observes it, Kargo may retain Freight
for that unadjudicated digest, but the exact-digest query fails closed: it
cannot advance the channel. A later soaked Freight with its own passing
verdict supersedes it on the next promoter cycle. This can delay a nightly; it
cannot turn missing evidence into a release.

The loopback server accepts one ephemeral key, listens only on `127.0.0.1`, and
will execute only the exact `sh -s -- <directory>` request expected from the
CLI. It needs no standing host, account or credential. Empty channel lanes are
the honest pre-release state: the canary heartbeats with zero active lanes.
The first qualified pin and published Release activate the continuous public
ring.

## Stable release process

Stable uses a release-candidate lineage and a single-source version discipline:

1. Confirm a recent release candidate and its source lineage.
2. Change `0.x.y-pre` to bare `0.x.y` in Cargo, Bazel and the lockfile in one
   reviewed PR. Packaging-lockstep tests must pass.
3. Let the main-only image workflow build, sign, attest and verify a new OCI
   digest. Read its tests before selecting the digest.
4. Confirm the exact digest's passing OpenSSH deep-test metric, then move the
   stable channel pin and matching release-manifest digest/version in one
   reviewed PR. The release workflow validates lineage, version, signature,
   SBOM and asset completeness, then builds an App-authored draft, uploads and
   reads back the exact assets, and publishes only after every byte matches.
   A failed attempt leaves a repairable draft and does not emit the registry
   lanes' `release: published` event.
5. Observe the public-distribution canary across GitHub, npm and crates.io. The
   mirrors must publish from that stable Release, not from a manually supplied
   local artifact.
6. Immediately return main to the next `-pre` version so subsequent edge and
   nightly bytes cannot impersonate the stable version.

No workflow runs privileged publication code for a pull-request checkout.
Build/sign/publish jobs run only after merge on reviewed main history, and the
trusted-publisher workflow filenames are security identities that must not be
renamed without registry re-registration.

## External release gates

The repository intentionally contains no publishing credential. Before the
first release, an owner must complete and verify these external bindings:

1. **Public GHCR package:** after the trusted main image workflow creates
   `ghcr.io/guardian-intelligence/pipe-to-remote-box`, a Guardian package or
   organization admin must make it public and verify that anonymous token and
   `:edge` manifest requests succeed. The canaries intentionally receive no
   registry credential.
2. **GitHub Releases:** verify the existing `guardian-promotions` GitHub App is
   installed on `guardian-intelligence/guardian` with repository Contents
   write, and that `PROMOTIONS_APP_ID` and `PROMOTIONS_APP_PRIVATE_KEY` expose
   that App to the release workflow. The App-authored Release is required so
   GitHub emits the `release: published` event that starts both registry lanes.
3. **npm:** claim the meta package and all four platform-package names, then
   register a trusted publisher on each package for repository
   `guardian-intelligence/guardian`, workflow
   `pipe-to-remote-box-publish-npm.yml`, no GitHub environment, permission
   `npm publish`. Remove any temporary bootstrap token and non-release dist-tag
   after registration.
4. **crates.io:** claim `pipe-to-remote-box`, then register its trusted
   publisher for repository `guardian-intelligence/guardian`, workflow
   `pipe-to-remote-box-publish-crates.yml`, with no GitHub environment. Revoke
   and log out any temporary least-privilege bootstrap token immediately after
   the OIDC binding exists.

These are identity and registry-state gates, not reasons to add token secrets,
weaken verification, publish from a pull request, or rename workflows. The
first release is blocked until the public GHCR path and all three publisher
bindings are rendered and verified in their respective interfaces.
