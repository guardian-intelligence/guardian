# Postflight CLI distribution: channels, signatures, and the cut ceremony

Status: active as of 2026-07-25. One release exists —
`postflight-cli/nightly-20260723` — and only the nightly channel is pinned.
The npm, crates.io and Homebrew lanes are written and inert: they fire on
stable tags only, and no stable tag exists.
Complements `supply-chain-design.md` (trust model, canonical identities),
`registry-design.md` (countersigner, release projector, the release
manifest) and `canaries.md` (canary principles).

The `postflight` binary is the only thing Guardian releases to users today.
Everything below exists to make one sentence true: **the bytes a user runs
were built by reviewed main history, and the signature that says so was
minted once, at build time, and never re-minted anywhere downstream.**

## Install matrix

Four targets are built and released every time: `x86_64-unknown-linux-musl`,
`aarch64-unknown-linux-musl`, `x86_64-apple-darwin`, `aarch64-apple-darwin`.

| Method | Command | Channels | Status |
|---|---|---|---|
| curl installer | `curl -fsSL https://guardianintelligence.org/postflight/install.sh \| sh -s -- --channel nightly` | stable (default), `--channel rc\|nightly` | live |
| release asset | download `postflight-<target>` from the release, `cosign verify-blob`, `chmod +x` | all | live |
| make | `make && sudo make install` in `src/products/postflight-cli` of a clone, or in an unpacked release source tarball | all | live |
| cargo | `cargo install postflight` | stable | lane wired, inert |
| cargo-binstall | `cargo binstall postflight` | stable | lane wired, inert |
| npm/bun | `npm i -g @guardian-intelligence/postflight` | stable | lane wired, inert |
| brew | `brew install guardian-intelligence/tap/postflight` | stable | lane wired, inert |
| mise | tool `ubi:guardian-intelligence/guardian` with `exe=postflight` and `tag_regex=^postflight-cli/v` | stable | untested |

Only the first three work today. "Lane wired, inert" means the publishing
machinery is on main and correct but has never run: all three ecosystem
lanes are filtered to non-prerelease `postflight-cli/v*` tags, no stable
release exists, and each is additionally waiting on a registry-side or
repository-side ceremony (see below). Because nightly is the only channel
with a release, the installer's default (`stable`) currently exits with the
channel-enumeration message rather than installing:

```
postflight: no release on the 'stable' channel yet. Available channels: nightly
```

The `mise` row is a recipe declared in the crate README; nothing in the
repository or the canary estate exercises it. Its `tag_regex` matches `v`
tags only, so it will not see nightlies. `cargo binstall` resolves
`{repo}/releases/download/postflight-cli%2Fv{version}/postflight-{target}`
from `[package.metadata.binstall]`, which likewise only exists for `v` tags
— but binstall still needs crates.io to resolve the version, so it lands
with `cargo install`, not before.

### What a release carries

The cutter stages, per release: the four `postflight-<target>` binaries,
their four `postflight-<target>.sigstore.json` bundles, a
`postflight-<version>-src.tar.gz` source tarball cut from the build commit
with `LICENSE.md` spliced in at the archive prefix, and a `checksums.txt`
generated last so its glob covers everything else. The one release published
so far predates the source tarball and carries only the binaries, the
bundles and `checksums.txt`.

The installer needs curl, sed, grep and either `sha256sum` or `shasum` —
no jq, because the releases JSON is parsed with anchored `sed` expressions
that key names smuggled into release notes cannot match. It always checks
the sha256 from `checksums.txt`; it runs `cosign verify-blob` when cosign is
on PATH and warns when it is not, and `--require-verification` turns that
warning into an error. It installs to `~/.local/bin` (or
`POSTFLIGHT_INSTALL_DIR`), so no step of the happy path needs sudo, and it
stages the binary under a temporary name inside the destination directory
and runs `postflight version` before giving it its final name — a download
that cannot execute never shadows a working install.

`make install` deliberately does not depend on the build target: the
documented flow is `make && sudo make install`, and building under sudo
would run cargo as root against root's `CARGO_HOME`.

## The channel ladder

```
edge (OCI, never user-facing) -> nightly -> rc -> stable
```

| Channel | Tag | Prerelease | Pin advanced by |
|---|---|---|---|
| edge | none (OCI tags `:edge` and `:sha-<commit>`) | n/a | the `postflight-cli-image` workflow, on every main push touching the crate |
| nightly | `postflight-cli/nightly-YYYYMMDD` | yes | Kargo promotion PR, fired daily at 08:00 UTC by `cli-nightly-promoter` |
| rc | `postflight-cli/v<version>-rc.<n>` | yes | a human PR |
| stable | `postflight-cli/v<version>` | no (`--latest`) | a human PR |

Nightly tags take a `-HHMM` suffix if the day's tag already exists at a
different commit; for rc and stable a tag that already points elsewhere
fails the cut instead.

**edge** is the OCI substrate and is never a user-facing channel. The lane
cross-compiles all four targets from one Linux runner (zig supplies the C
toolchain that `ring` needs on every target), signs each binary, and pushes
a single OCI artifact to `ghcr.io/guardian-intelligence/postflight-cli`.
Binaries are byte-reproducible — `--remap-path-prefix` erases build-machine
paths, and no commit is stamped into the binary, which is why
`CARGO_PKG_VERSION` is the only thing that varies — so the lane hashes the
four binaries into an `org.guardian.binaries-digest` annotation and skips
the push entirely when `:edge` already holds that hash. Every Kargo Freight
therefore represents a real change. Commit provenance lives in the manifest
annotations (`org.opencontainers.image.revision`) and the Fulcio
certificate, not in the binary.

Channel-pin PRs touch only `release/`, which the lane's path filter
excludes, so promoting a pin never rebuilds the artifact it pins.

**nightly** is automatic but not immediate. Kargo's own auto-promotion fires
the moment Freight is discovered, which would make the word "nightly" a lie,
so the `postflight-cli-nightly` Stage carries no promotion policy and a
CronJob in `guardian-products` creates the Promotion CR once a day instead.
Promotion itself stays entirely Kargo's: the job composes the Stage's
promotion template inline (Kargo's webhook does not copy template steps into
directly-created Promotions) and the controller runs the pin-bump PR,
automerge and reconciler as usual. The job refuses to stack a second
promotion while one is Pending or Running.

**rc** and **stable** have no Kargo stage. Their pins land by hand-authored
PR, which is why the cutter reads `channels.yaml` with a real YAML parser: a
trailing comment, a quoted value or a typo'd channel key must fail the cut,
not silently resolve to "no pin". A channel with no pin is skipped, so the
`rc` and `stable` keys can appear the day the first candidate is cut.

The cutter dedups per channel by tag prefix against the digests recorded in
existing release bodies, so an rc-only pin edit cannot re-cut nightly.

## The sign-once contract

Signatures are minted in exactly one place: the `postflight-cli-image`
workflow, on a main push. Per run it produces

- one `cosign sign-blob` bundle per binary, written into the artifact layer
  next to the binary it covers, so the bundle travels with the bytes;
- one `cosign sign` over the OCI artifact digest;
- one `cosign attest --type spdxjson` SBOM attestation over the same digest.

It then verifies all three exactly as a consumer would before the job ends.

The canonical identity — pin it verbatim — is

```
https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main
```

with OIDC issuer `https://token.actions.githubusercontent.com`. It is the
same string carried by `supply-chain-design.md`, the countersigner's
identity map, the deep-test runner, the install canary, the installer script
and every release's notes.

**Promotion never re-signs.** The cutter `crane export`s the pinned
artifact, copies the bundles out of the layer, re-verifies every one of them
against the identity above, and publishes them as release assets unchanged.
Nothing downstream of the build has a signing capability, and there is no
key for it to have.

A user verifies a downloaded asset with:

```sh
cosign verify-blob --bundle postflight-x86_64-unknown-linux-musl.sigstore.json \
  --certificate-identity https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  postflight-x86_64-unknown-linux-musl
```

That is the command the release notes print, the command the installer runs
when cosign is on PATH, and the command the install canary runs against
every published asset every six hours.

Guardian's own countersignature is a second, independent signature over the
same digests. It is minted in-cluster from `openbao://guardian-images`, and
the release projector copies subject and countersignature to ghcr — which is
why the release manifest must move in lockstep with the channel pin (see
below). The countersigner and projector are `registry-design.md`'s subject;
what matters here is that neither of them touches the per-binary bundles a
CLI user verifies.

## Ecosystem mirrors

Three lanes mirror a **stable** release into the package managers users
already run. All three are gated on
`startsWith(tag_name, 'postflight-cli/v') && !prerelease`, so nightly and rc
cuts fire nothing, and none of them signs anything: npm republishes the
release's signed bytes after re-verifying them, Homebrew pins those bytes by
the checksums the cutter generated from them, and crates.io ships the source
they were built from. npm and crates.io hold no token secrets at all —
trusted publishing mints their credential over OIDC — and the tap write uses
a short-lived App token.

| Lane | Where it lives | Trigger | Auth |
|---|---|---|---|
| npm | `.github/workflows/postflight-cli-publish-npm.yml` | `release: published` | npm trusted publishing (OIDC) |
| crates.io | `.github/workflows/postflight-cli-publish-crates.yml` | `release: published` | crates.io trusted publishing (OIDC), via `rust-lang/crates-io-auth-action` |
| Homebrew | a step in the stable path of `postflight-cli-release.yml` | inline, `contains(cuts, 'stable')` | a second short-lived `guardian-promotions` App token |

`release: published` only fires because the cutter authenticates as an App
rather than as `GITHUB_TOKEN` — releases authored by `GITHUB_TOKEN` do not
trigger workflows.

### Why Homebrew is a step and not a fourth workflow file

`.github/workflows/AGENTS.md` closes the workflow set: a file earns a place
there for exactly two reasons, the singular merge-time gate or a **trusted
publisher identity**, and it names `postflight-cli-publish-npm.yml` and
`postflight-cli-publish-crates.yml` as never-rename identities — npm and
crates.io authorize by org/repo/workflow-filename, so a rename silently
revokes publish rights with nothing in the Bazel graph to catch it.

The tap write has no such property: it authenticates by App key, not by
filename. It is promotion glue, so it does not earn a file, and its render
logic lives in the Bazel graph instead —
`src/products/postflight-cli/dist/homebrew/render-formula.sh` plus
`postflight.rb.tmpl`, covered by `:render_formula_test`, reachable from
`//...`.

### npm

Five packages, in the esbuild shape: the meta package
`@guardian-intelligence/postflight`, whose `bin/postflight` is a Node shim
that resolves the platform binary through `optionalDependencies`, plus four
platform packages — `@guardian-intelligence/postflight-{linux-x64,linux-arm64,darwin-x64,darwin-arm64}`
— each carrying its own `os`/`cpu` so npm picks exactly one. There is no
install script.

The templates in `dist/npm/` hold version `0.0.0-dev` and no binaries. The
lane downloads the release assets, runs `sha256sum -c checksums.txt` and
`cosign verify-blob` against the build identity, and only then installs the
verified bytes into the platform packages and rewrites every `version` and
every `optionalDependencies` pin to the release version. Platform packages
publish before the meta package, because an install that won the race
between the two would resolve nothing to run; a re-run after a partial
publish converges past `EPUBLISHCONFLICT` and still fails on anything else,
since npm versions are immutable.

Two guards worth knowing. The packaging sources are checked out from the
**default branch**, not the release tag: the cutter tags at the image's
`org.opencontainers.image.revision` — the commit whose binary bytes last
changed — which can sit hundreds of commits behind main and predate these
templates entirely. And before anything publishes, the packed linux-x64
binary must report `postflight version <tag version>`, because an immutable
registry is a bad place to discover that a version bump never rode the
freight.

### crates.io

The one lane that ships something other than the signed release bytes:
crates.io is a source registry. It checks out **at the release tag** (there
the checked-out source is the artifact), installs the toolchain pinned in
`rust-toolchain.toml`, asserts that `cargo metadata`'s version equals the
version in the tag, mints a short-lived token through trusted publishing,
and runs `cargo publish --locked -p postflight`.

`Cargo.toml` still carries `publish = false`. That is the remaining inert
guard, not an oversight — the lane cannot publish until it is flipped, which
is part of the crates.io ceremony below.

### Homebrew

A binary formula, no source build. The cutter's stable path re-asserts that
the artifact reports the version the formula will promise (the formula's own
`test do` block asserts it back at install time, on a user's machine — this
moves that discovery off the user's machine), renders `postflight.rb` from
the template and the release's own `checksums.txt`, and PUTs it to
`Formula/postflight.rb` in `<owner>/homebrew-tap`, passing the previous blob
sha when one exists.

The renderer derives its target list from the template's own download URLs,
so it never carries a second copy of the target set, and it fails if any
`@PLACEHOLDER@` survives substitution.

The tap write mints its **own** App token. `create-github-app-token` scopes
an installation token to the current repository by default, so the token
that cuts the Release 404s against the tap; the tap token names
`repositories: homebrew-tap` explicitly. It is minted *before* the cut, so a
tap that is unreachable fails the release rather than stranding it
half-published.

### One target set, six surfaces

Adding a target to the build lane alone is silent — nothing is red, nothing
pages, and the gap reaches users as "no prebuilt binary for your platform"
from a release that has one. `TestPostflightCliReleaseTargetsMoveTogether`
(`src/infrastructure/tests/postflight_cli_targets_lockstep_test.go`) is the
only thing preventing that: it binds the image lane's `TARGETS` to the
cutter's `TARGETS`, the npm lane's `PLATFORMS` map, the bin shim's dispatch
keys and the package names it resolves, the meta package's
`optionalDependencies`, each platform package's own name and `os`/`cpu`, and
the formula template's per-arch download URLs.

## Verification estate

Three loops, all labelled `guardian_component: cli-release-train`.

### Pre-promotion: `cli-deeptest-runner`

Hourly at `:20` in `guardian-analytics`, offset well clear of the 08:00
promotion so the gate reads a verdict rather than racing one. It resolves
`:edge` anonymously, computes the digest from the manifest bytes in hand
rather than trusting a header, and records these checks:

| check | assertion |
|---|---|
| `edge_resolved` | `:edge` resolves and its manifest is readable |
| `layers` | every layer blob matches its digest and all four targets are present with a bundle each |
| `binaries_digest` | the four binaries re-hash to the manifest's `org.guardian.binaries-digest` |
| `image_signature` | `cosign verify` against the canonical identity |
| `blob_signatures` | `cosign verify-blob` on all four bundles |
| `structure` | object magic plus the machine/cputype word matches the target the directory name claims |
| `version` | the native binary prints `postflight version <semver>` |
| `device_flow` | a real device-code request against the live issuer prints its one-time code |

`structure` is read-only inspection and runs unconditionally. Execution does
not: if `image_signature`, `blob_signatures` or `binaries_digest` is 0 the
runner refuses to run the artifact at all, and every CLI invocation runs
under `env -u CLICKHOUSE_USER -u CLICKHOUSE_PASSWORD` so a binary that does
run never sees the pod's credential.

The verdict is data, not a Job outcome — a clean 0 exits zero, and only an
inability to produce a verdict fails the Job.

Series: `guardian_cli_deeptest_pass{digest}`,
`guardian_cli_deeptest_check{digest,check}`,
`guardian_cli_deeptest_edge_seen_seconds{digest}`,
`guardian_cli_deeptest_heartbeat`, `guardian_cli_deeptest_event_write`, plus
report-only `guardian_cli_deeptest_startup_ms{digest,stat}`,
`guardian_cli_deeptest_auth_rtt_ms{digest}` and
`guardian_cli_binary_size_bytes{digest,target}`. No baselines exist for the
report-only ones, so none of them moves the verdict. Events
`cli.deeptest.run` and `cli.deeptest.benchmark` land in
`guardian_analytics.events` under site `postflight-cli`.

`guardian_cli_deeptest_edge_seen_seconds` exists so the alert can tell the
current subject apart from digests that later commits have superseded: a
digest keeps its verdict series for the whole window, and only the digest a
promotion could still pick up is worth paging on.

### Post-release: `cli-install-canary`

Every six hours at `:40`, over the public internet. It reads the channel
lanes from a generated copy of the release manifest — so an rc or stable
lane is covered the day its pin lands, with no second place to teach —
resolves each channel's newest release by tag grammar over the full paginated
release listing, and asserts lineage first: **the newest release on a channel
must carry the pinned digest in its body.** A mismatch means the train
published something other than what is pinned, and fails every method on that
channel rather than skipping it.

Per channel × method cell it downloads every asset, runs `sha256sum -c
checksums.txt`, runs `cosign verify-blob` on all four binaries with online
Sigstore trust-root resolution (the point of this canary is that
verification works for a reader of the release page, not that it works
against our pinned root), executes the linux binary for its version, and
checks object magic on the three it cannot run.

Series: `guardian_cli_install_canary_success{channel,method,platform}`,
`guardian_cli_install_canary_release_age_seconds{channel,platform}`,
`guardian_cli_install_canary_heartbeat`. Events:
`cli.install_canary.method`. `platform` is `linux-x86_64-cluster` — the
label exists so a satellite runner on other hardware can report into the
same series.

A failing cell is reported, not thrown: the Job only fails when the sweep
was hollow or its ClickHouse records did not land, because a Job exit code
would page on the first transient public-internet hiccup.

### Cadence: `cli-nightly-promoter`

Heartbeats `guardian_cli_nightly_promoter_heartbeat{stage}` on every run
including its no-op paths, and `guardian_cli_nightly_promotion_created{stage}`
when it actually creates a Promotion.

### The gate: `check-deeptest-pass`

The nightly Stage's promotion template opens with an `http` step that asks
vmselect for `guardian_cli_deeptest_pass` at the exact digest being promoted
— the Kargo controller queries `vmselect-shortterm` from its own pod, so
nothing outside the cluster is involved. A verdict of 1 promotes; a verdict
of 0 fails the Promotion terminally and no pin PR is ever opened. The two
expressions are complements over a result that exists, so a digest with no
verdict yet — one built between the runner's last hourly pass and 08:00 —
matches neither and the step retries until the next run adjudicates it,
bounded by `retry.timeout: 28m`. That budget spans a full runner cadence and
stays under the 30 minutes at which `KargoPromotionStuck` warns; a digest
that is still unadjudicated when it runs out errors the Promotion, which
`KargoPromotionFailed` already pages on. No new alert exists for the gate,
deliberately: an Errored Promotion is the signal.

The promoter needs no knowledge of any of this. It copies the Stage's
`.spec.promotionTemplate.spec` vars and steps into the Promotion it creates
verbatim, so a step added to the template is a step the next promotion runs.

### Alerts

| Alert | Severity | Fires when |
|---|---|---|
| `GuardianCliInstallCanaryFailing` | critical | the same channel × method cell fails twice consecutively (13h window, ≥2 samples) |
| `GuardianCliInstallCanarySilent` | warning | no install-matrix heartbeat for 13h |
| `GuardianCliDeeptestFailing` | warning | `guardian_cli_deeptest_pass` is 0 for the digest `:edge` currently points at |
| `GuardianCliDeeptestSilent` | warning | no deep-test heartbeat for 3h |
| `GuardianCliDeeptestEventWriteFailing` | warning | the runner's ClickHouse INSERT is being rejected |
| `GuardianCliNightlyPromoterSilent` | warning | no promoter heartbeat for 26h |

Only the install canary pages critical, and deliberately: it is the only
loop watching what a user actually hits.

## Cutting an rc, then a stable

Both cuts are the same shape. Steps 1–3 and 5 are identical; step 4 is
required only for an rc, step 6 differs in the assertions the cutter runs,
and step 7 happens only for a stable.

1. **Bump the version.** Edit `version` in
   `src/products/postflight-cli/Cargo.toml` *and* `CARGO_PKG_VERSION` in
   `src/products/postflight-cli/BUILD.bazel` in the same PR — three files
   independently name and version this binary, and
   `//src/products/postflight-cli:packaging_lockstep_test` fails if the two
   disagree. For a candidate the version is `0.2.0-rc.1`; for the stable
   that follows it, `0.2.0`. Nothing else carries a version to bump — the
   npm package templates hold `0.0.0-dev` and the formula template holds
   `@VERSION@`, both injected at publish time from the tag. Merge.

2. **Let edge rebuild.** The merge touches the crate, so
   `postflight-cli-image` runs. The version change alters
   `CARGO_PKG_VERSION`, therefore the bytes, therefore the binaries digest,
   so the dedup does not skip and `:edge` moves to a new digest.

3. **Wait for a deep-test verdict.** The runner picks the new `:edge` up
   within the hour. Only nightly's Kargo promotion gates on that verdict —
   a hand-authored rc or stable pin does not — so read it before writing the
   pin: it is the check that would have caught a broken cross-compile before
   anyone published it.

4. **Wait for nightly** — required for an rc, optional for a stable. The
   08:00 promoter opens the pin PR; once it merges, the cutter publishes
   `postflight-cli/nightly-<YYYYMMDD>` from that digest. An rc's lineage
   assert requires the digest to appear in a nightly release body, so an rc
   cannot be cut before its nightly exists. A stable's lineage is a version
   assert (step 6), so it does not need one.

5. **Open the pin PR, editing both files in one commit.**

   `src/products/postflight-cli/release/channels.yaml`:

   ```yaml
   channels:
     rc:
       image: ghcr.io/guardian-intelligence/postflight-cli@sha256:<edge digest>
       version: 0.2.0-rc.1
   ```

   `src/infrastructure/deployments/guardian/system/release-manifest.yaml`:

   ```yaml
   releases:
     postflight-cli:
       rc:
         image: ghcr.io/guardian-intelligence/postflight-cli@sha256:<same digest>
   ```

   Both, in one commit, for two reasons. Mechanically,
   `TestReleaseManifestCoversReleaseChannels` fails the PR if a channel pins
   a digest the manifest does not list, or if the manifest lists one no
   channel pins. Substantively, the manifest is what puts the digest under
   the countersigner and the release projector: a channel pin alone would
   publish an artifact to users that carries no Guardian countersignature at
   the public registry, which is the one invariant the publication boundary
   holds. Kargo's nightly promotion template carries the second
   `yaml-update` for exactly this reason; a hand cut has to do it by hand.

6. **Merge, and the cutter fires.** `postflight-cli-release` runs on any
   main push touching `channels.yaml`. Per newly-pinned channel it asserts,
   in order:

   - the pin parses and names a known channel;
   - the digest has not already been released on this channel;
   - **lineage.** For rc: the digest must already appear in a nightly
     release body — the same bytes, one rung down. For stable: a release
     tagged `postflight-cli/v<version>-rc.*` must already exist, and
     `compare(<that tag's commit>...<build commit>)` must be `ahead` or
     `identical`;
   - the artifact's `cosign verify` and all four `verify-blob`s pass;
   - **version match.** The pinned native binary's `postflight version …`
     output must equal the `version` in the pin. That output is what names
     the tag, so a pin claiming a version the bytes do not carry cannot mint
     a misnamed release.

   It then stages assets, mints a short-lived `guardian-promotions` App
   token, creates the tag ref, and cuts the release. The App token is minted
   *after* the dedup check and is not `GITHUB_TOKEN` on purpose:
   App-authored releases fire `release:` workflows, which is what the npm
   and crates.io lanes ride. The tag ref is created before the release
   because the releases API 403s integration tokens when
   `target_commitish` is a bare SHA (cli/cli#9514).

7. **A stable cut keeps going.** Publishing the release fires
   `postflight-cli-publish-npm` and `postflight-cli-publish-crates`, and the
   cutter's own stable path renders the Homebrew formula and mirrors it to
   the tap. An rc cut fires none of them. Watch all three: they are the
   first thing users touch, and none of them is covered by the install
   canary, whose methods are the release assets and the curl installer.

### Why stable's lineage is a version assert, not a digest match

An rc promotes nightly's exact bytes, so "has this digest been released on
nightly" is a real question with a real answer, and rc's lineage is a digest
grep.

Stable cannot work that way. Dropping the `-rc.N` suffix changes
`CARGO_PKG_VERSION`, which changes the binary's bytes, which changes the
binaries digest, which changes the OCI digest. **Stable can never share a
digest with the candidate it promotes.** A symmetric digest-lineage rule
would make a stable cut unsatisfiable.

So stable asserts the two things that remain checkable: that a candidate for
this exact version was actually released, and that the commit which built
the stable bytes descends from the commit that built the candidate. What is
given up is honest to state — the stable binary is not the artifact that
soaked as an rc, only a rebuild of the same sources with the suffix removed.

## Ceremonies still owed before first stable

The lanes are written. What is missing is on the other side of each
registry: an account, a repository, or a trust registration that only a
human with those credentials can create. Every one of them fails the lane
loudly rather than silently publishing nothing, so none of them can be
discovered late by a user.

- **crates.io.** Two things. The crate name `postflight` has to be owned on
  a flat namespace and a trusted publisher registered against
  `postflight-cli-publish-crates.yml` in this repository, and `Cargo.toml`'s
  `publish = false` has to be flipped — `cargo publish` refuses outright
  while it stands. Both `cargo install postflight` and
  `cargo binstall postflight` are downstream of this; binstall's asset URL
  is already correct, it is the index lookup that is missing.

- **npm.** Trusted publishing is registered *per package*, so that is five
  registrations against `postflight-cli-publish-npm.yml`: the meta package
  and each of the four platform packages. The lane asserts npm ≥ 11.5.1
  before it tries, because older npm cannot publish without a token and the
  point of this lane is that it holds none.

- **Homebrew.** The `<owner>/homebrew-tap` repository has to exist — brew's
  naming convention is what makes
  `brew install guardian-intelligence/tap/postflight` resolve — and the
  `guardian-promotions` App has to be installed on it with Contents read
  **and** write. Read alone is enough to see the formula, not to PUT it.
  The cutter mints that token by name (`repositories: homebrew-tap`), so an
  App that is not installed there fails the token step before the release is
  cut rather than after.

- **The Actions allowlist.** `rust-lang/crates-io-auth-action` is new
  third-party surface and is pinned in `.github/actions-allowlist.json`. The
  repository setting is the enforcing half and lives outside Git: it needs
  re-applying, or every workflow using the action dies as a
  `startup_failure` (`docs/dependency-management.md`).

- **The `cli_canary` OpenBao relay.** The ClickHouse user is declared on the
  app CR and the chart generates its password into
  `tenant-root/clickhouse-analytics-credentials`; the operator relays that
  value once into
  `kv/guardian/guardian-mgmt/guardian-analytics/clickhouse` property
  `cli_canary`, from where ESO materializes
  `guardian-analytics/cli-release-canary`. A kv write replaces the whole
  secret, so `ingest`, `payments_canary` and `cli_canary` go in one write.
  The procedure is in `runbooks/analytics-clickhouse.md`; it must be re-run
  for every user after any DR rebuild of the app.

  Until it is done the two loops fail differently, which is worth knowing
  before reading a red dashboard: the deep-test runner keeps producing
  verdicts and metrics and only loses its event history
  (`GuardianCliDeeptestEventWriteFailing` warns), while the install canary's
  Job exits non-zero because a failed event write is one of the two
  conditions it treats as a hollow run.

## Known gaps

**No macOS execution coverage anywhere.** Both darwin targets are
cross-compiled on a Linux runner, released, packed into npm platform
packages, pinned by the Homebrew formula, and verified at every step — but
never run. The deep-test runner checks Mach-O magic plus the cputype word;
the install canary checks magic only; every version assert in the estate
runs the linux-x64 binary because it is the only one the runner can
execute. A cross-compile that produced a correct-looking Mach-O that faults
on first instruction would pass every gate we have, and the formula's own
`test do` block would be the first thing to notice — on a user's machine.
The honest closer is a satellite canary on real Apple hardware reporting
into the same series; the `platform` label on
`guardian_cli_install_canary_success` exists for it, and the `cli.` event
prefix is already registered so a satellite can speak the same vocabulary
through the public path.

**Nothing canaries the ecosystem mirrors.** The install canary's methods are
the release assets and the curl installer. A published npm version that
resolves nothing to run, a crate that stopped building on a fresh
toolchain, or a tap formula whose checksums drifted would be found by a
user, not by us. The lanes cannot be exercised at all until a stable
release exists, so this is deliberately unbuilt rather than deferred — but
it is the first thing to add once one does.

**`cli_canary` holds estate-wide ClickHouse rights.** The release canaries
write their own `cli.*` events, so the account cannot be `readonly` like the
payments checkout canary's — and `readonly` is the chart's only restriction
knob. Narrowing it to INSERT on `guardian_analytics.events` afterwards is
not possible: chart-declared users are rendered into the server's
`users.xml`, whose access storage refuses every GRANT and REVOKE
(`Code: 495 ACCESS_STORAGE_READONLY`, reproduced against
clickhouse-server 24.9.2.42). The account therefore carries the same
estate-wide DDL rights `ingest` does. Accepted deliberately, on two
structural grounds: the runner that holds the credential never executes an
artifact whose signature chain failed, and it strips the credential from the
environment of every artifact it does execute. Closing it needs either an
upstream `<grants>` passthrough on the chart's user block or SQL-created
users the chart does not own.
