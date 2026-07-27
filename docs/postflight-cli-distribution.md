# Postflight CLI distribution: channels, signatures, and the cut ceremony

Status: active as of 2026-07-27. Every user-facing channel has published:
stable is `postflight-cli/v0.2.0`, and the npm, crates.io and Homebrew lanes
all mirrored it.
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
| cargo | `cargo install postflight` | stable | live |
| cargo-binstall | `cargo binstall postflight` | stable | live |
| npm/bun | `npm i -g @guardian-intelligence/postflight` | stable | live |
| brew | `brew install guardian-intelligence/tap/postflight` | stable | live |
| mise | tool `ubi:guardian-intelligence/guardian` with `exe=postflight` and `tag_regex=^postflight-cli/v` | stable | untested |

All three ecosystem mirrors hold 0.2.0, published from the
`postflight-cli/v0.2.0` release. crates.io additionally carries 0.1.0, hand
published to claim the name before the lane existed; versions there are
immutable, so both numbers are spent and every stable must climb past them.

Removal is `postflight self uninstall` for every method that leaves an
install receipt, which today means the curl installer; each package manager
owns removal of the copy it placed, and the CLI declines those rather than
desyncing it. See [Uninstall](#uninstall).

The `mise` row is a recipe this document declares and nothing exercises —
not the repository, not the canary estate. Its `tag_regex` keeps `ubi` off
the nightlies, but `^postflight-cli/v` is looser than "stable": the listing
holds one candidate spelled that way, `postflight-cli/v0.2.0-rc.1`, from
before the `rc-` prefix existed. `^postflight-cli/v[0-9.]+$` is the pattern
that means what the row intends. `cargo binstall` resolves
`{repo}/releases/download/postflight-cli%2Fv{version}/postflight-{target}`
from `[package.metadata.binstall]` and takes the version from crates.io, so
it lands exactly where `cargo install` does.

### What a release carries

The cutter stages, per release: the four `postflight-<target>` binaries,
their four `postflight-<target>.sigstore.json` bundles, a
`postflight-<version>-src.tar.gz` source tarball cut from the build commit
with `LICENSE.md` spliced in at the archive prefix, `install.sh` with its own
`install.sh.sigstore.json`, and a `checksums.txt` generated last so it covers
everything else. `postflight-cli/nightly-20260723`, the first release ever
cut, predates the source tarball and the installer and carries only the
binaries, the bundles and `checksums.txt`.

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

Five properties exist because the intended delivery is a pipe into `sh`:

- **Truncation is inert.** Every statement lives inside `main`, and the call to
  it on the last line is braced — `{ main "$@"; }` — so that no prefix of the
  script is a runnable command. Bare `main "$@"` is not enough: a stream cut
  between the name and its arguments leaves `main`, which is complete, and
  installs whatever the default channel points at from a script the reader only
  ever saw half of. An unterminated brace group is a syntax error, and a syntax
  error runs nothing. `installer_test` sweeps every proper prefix — 64-byte
  strides across the body, then byte by byte across the last 512, because the
  tail holds the only statement that executes anything and a coarse stride
  stepped straight over this one — under every POSIX shell on the box, and
  asserts no prefix creates an install directory, writes to stdout, or reaches
  the download.
- **A re-run upgrades in place.** With no `POSTFLIGHT_INSTALL_DIR`, the
  destination comes from the existing receipt when it names a binary that is
  still there. Otherwise someone who installed into `/usr/local/bin` and later
  re-ran the one-liner would get a second copy in `~/.local/bin`, a receipt
  describing only that copy, and the original still ahead of it on `PATH` — an
  upgrade that changes nothing they run, and reports success.
- **`--version <x.y.z>` pins.** It resolves `postflight-cli/v<x.y.z>`
  directly, consulting no listing, so a CI job gets the same bytes on every
  run. It is mutually exclusive with `--channel`.
- **sudo is refused for a fresh install.** `SUDO_USER` set with no explicit
  `POSTFLIGHT_INSTALL_DIR` is an error: `HOME` under sudo is root's, so the
  install would land where the person who typed it will never look. Naming a
  destination makes a shared install deliberate; so does upgrading a recorded
  installation, whose destination came from the receipt rather than from whose
  home the shell happens to have.
- **Only failures speak.** cosign narrates to stderr on success as well, so
  its output is captured and printed only when verification fails.

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
| nightly | `postflight-cli/nightly-YYYYMMDD` | yes | Kargo promotion PR, fired by `cli-nightly-promoter` at its next departure (see Cadence) |
| rc | `postflight-cli/rc-YYYYMMDD` | yes | a human PR |
| stable | `postflight-cli/v<version>` | no (`--latest`) | a human PR |

Nightly and rc tags take a `-HHMM` suffix if the day's tag already exists at
a different commit; for stable a tag that already points elsewhere fails the
cut instead.

**The tag is the channel.** Nothing inside a release records which channel it
was cut on, so the three prefixes — `nightly-`, `rc-`, `v` — have to
partition the release space, and every consumer resolves a channel by them:
the cutter dedups and walks lineage with them, `install.sh` resolves
`--channel` with them, and the install canary picks each channel's newest
release with them. One tag in the listing predates the grammar —
`postflight-cli/v0.2.0-rc.1`, the candidate 0.2.0 was promoted from — and
registry history is permanent, so all three read that shape as a candidate
too, and none of them lets it into the stable channel. No later cut can
produce another.
`TestPostflightCliChannelGrammarMovesTogether`
(`src/infrastructure/tests/postflight_cli_channel_grammar_test.go`) evaluates
each consumer's own matcher against one table of tag shapes; it is the only
thing keeping the three from drifting apart quietly, each still reporting
success against a release the others would not have picked.

**Three of the four rungs are the same bytes.** edge → nightly → rc is a
pointer move: the artifact that soaked is the artifact that promotes, and
nothing about it changes on the way up. Only the stable cut builds something
new, because dropping the prerelease suffix from the crate version changes
`CARGO_PKG_VERSION` and therefore the bytes. main names the *next* stable
with a `-pre` suffix, so a nightly or a candidate reports `0.3.0-pre` — the
version 0.3.0 would be cut from — and a bare `0.3.0` exists only on the one
commit a stable is built from. Which channel a binary came down, and which
day it was promoted, live in the release tag and the install receipt, never
in the binary.

**edge** is the OCI substrate and is never a user-facing channel. The lane
cross-compiles all four targets from one Linux runner (zig supplies the C
toolchain that `ring` needs on every target), smoke-tests what it built (see
[the edge smoke gate](#in-lane-the-edge-smoke-gate)), signs each binary, and
pushes a single OCI artifact to
`ghcr.io/guardian-intelligence/postflight-cli`. Binaries are
byte-reproducible — `--remap-path-prefix` erases build-machine
paths, and no commit is stamped into the binary, which is why
`CARGO_PKG_VERSION` is the only thing that varies — so the lane hashes the
four binaries into an `org.guardian.binaries-digest` annotation and skips
the push entirely when `:edge` already holds that hash. Every Kargo Freight
therefore represents a real change. Commit provenance lives in the manifest
annotations (`org.opencontainers.image.revision`) and the Fulcio
certificate, not in the binary.

Channel-pin commits touch only `release/` and the release manifest, which
the lane's path filter excludes, so promoting a pin never rebuilds the
artifact it pins.

**nightly** is automatic but not immediate. Kargo's own auto-promotion fires
the moment Freight is discovered, which would make the word "nightly" a lie,
so the `postflight-cli-nightly` Stage carries no promotion policy and a
CronJob in `guardian-products` creates the Promotion CR once a day instead.
Promotion itself stays entirely Kargo's: the job composes the Stage's
promotion template inline (Kargo's webhook does not copy template steps into
directly-created Promotions) and the controller pushes the pin-bump commit
to main and hands off to the reconciler as usual. The job refuses to stack
a second promotion while one is Pending or Running.

**rc** and **stable** have no Kargo stage. Their pins land by hand-authored
PR, which is why the cutter reads `channels.yaml` with a real YAML parser: a
trailing comment, a quoted value or a typo'd channel key must fail the cut,
not silently resolve to "no pin". A channel with no pin is skipped.

The cutter dedups per channel by tag grammar against the digests recorded in
existing release bodies, so an rc-only pin edit cannot re-cut nightly.

## The sign-once contract

Build outputs are signed in exactly one place: the `postflight-cli-image`
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

**Promotion never re-signs a build output.** The cutter `crane export`s the
pinned artifact, copies the bundles out of the layer, re-verifies every one
of them against the identity above, and publishes them as release assets
unchanged. There is no key anywhere for it to re-sign with.

The one asset the cutter signs itself is `install.sh`, under a second
identity:

```
https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-release.yml@refs/heads/main
```

The installer is a source file rather than a build output. It tracks main,
not the commit that produced the binaries — those can be many commits apart,
because the edge lane dedups on built bytes and skips the push when a change
leaves them identical. Signing it at build time would therefore ship a stale
installer with every release whose binaries had not changed. The cutter
copies it out of its own checkout, signs it, and verifies its own signature
before publishing; a release that cannot verify its installer does not
happen.

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

### Verifying the installer before running it

Piping a URL into a shell means executing code the website served, and no
script can vouch for itself. The answer is not to defend the pipe but to
make an unpiped path first-class: every release carries `install.sh` and
`install.sh.sigstore.json`, so the whole chain can be walked without
extending any trust to `guardianintelligence.org`.

```sh
tag=postflight-cli/v1.2.3
base="https://github.com/guardian-intelligence/guardian/releases/download/${tag//\//%2F}"
curl -fsSLO "$base/install.sh" && curl -fsSLO "$base/install.sh.sigstore.json"
cosign verify-blob --bundle install.sh.sigstore.json \
  --certificate-identity https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  install.sh
sh install.sh --version 1.2.3
```

The served copy and the released copy are the same bytes at the same commit
— `installer_lockstep_test` holds `dist/install.sh` and the site's
`public/postflight/install.sh` byte-identical — so a signature that fails
against a file fetched from the website means the website is serving
something the release lane never signed.

The install canary verifies this bundle every six hours, on any release whose
notes offer the recipe.

Guardian's own countersignature is a second, independent signature over the
same digests. It is minted in-cluster from `openbao://guardian-images`, and
the release projector copies subject and countersignature to ghcr — which is
why the release manifest must move in lockstep with the channel pin (see
below). The countersigner and projector are `registry-design.md`'s subject;
what matters here is that neither of them touches the per-binary bundles a
CLI user verifies.

## The install receipt

Sign-once has a cost that lands on the user: because the same bytes ride edge
→ nightly → rc, the binary cannot name the release it was published as. Its
version is the crate version at the build commit — the next stable with a
`-pre` suffix — identical across every prerelease channel and, between
version bumps, identical across every nightly. A binary that stamped its
channel or its tag would fork per channel, and the artifact that promoted
would no longer be the artifact that was tested.

So provenance is recorded beside the binary rather than inside it.
`install.sh` writes `install-receipt.json` into the CLI's config directory
(`$XDG_CONFIG_HOME/postflight`, else `~/.config/postflight`) — and under sudo,
into the *invoking* user's config rather than root's, handed back to them with
`chown`. A receipt in root's home describes an install nobody can see: the
user's own CLI reads its config from their home, so `postflight version` would
report no provenance and `self uninstall` would refuse to remove a binary this
script did in fact install. A root-owned receipt is worse still — readable and
never writable, so an upgrade reports success while the provenance still
describes the release before it.

```json
{
  "schema": 1,
  "method": "install.sh",
  "binary_path": "/home/you/.local/bin/postflight",
  "channel": "nightly",
  "tag": "postflight-cli/nightly-20260726",
  "version": "0.3.0-pre",
  "target": "x86_64-unknown-linux-musl",
  "binary_sha256": "a5211ec7…",
  "installed_at": "2026-07-26T05:24:16Z"
}
```

`postflight version` prints the tag, channel and install time underneath the
version line; `postflight version --short` prints the bare version for
scripts. **The first line of `postflight version` is fixed** — the deep-test
runner and the install canary both read it — so provenance is added below it
and never woven into it.

A receipt is evidence about the one path in `binary_path`, compared with
symlinks resolved. A second install elsewhere, or a `cargo install` copy
alongside an installer-managed one, leaves the old receipt in place; it does
not describe the new binary and is ignored rather than borrowed. A receipt
whose `schema` is newer than the reader understands is ignored too.

Failing to write a receipt never fails an install: the binary is already in
place and working, and provenance is not the install. A pinned `--version`
records no channel, because it follows none.

## Uninstall

`postflight self uninstall` sweeps the whole machine, removes everything the
installer put there — binaries, credentials, receipt, and the config directory
if nothing else is in it — and reports every copy it was not entitled to touch
alongside the command that removes each.

The sweep looks at `$PATH`, the homes each install method actually uses
(`~/.local/bin`, `/usr/local/bin`, `/opt/homebrew/bin`,
`/home/linuxbrew/.linuxbrew/bin`, `$CARGO_HOME/bin`), the path the receipt
records, and the running binary — that last one because it may be in none of
the others, having been run from a build directory. Results are deduplicated by
**device and inode**, so a file reached by several names is one installation:
without that, a Homebrew keg and the `bin` symlink into it read as a double
install and the same inode gets offered for removal twice.

Searching `$PATH` alone fails in both directions. A copy in a directory the
user has since dropped from `PATH` is invisible to `which` and still installed;
and a Homebrew copy *shadowed* by a newer one in `~/.local/bin` is exactly the
situation worth reporting, because after a successful uninstall it is what
`postflight` still resolves to — which is how someone concludes the uninstall
silently failed.

Four properties are load-bearing:

**It ends the session, not just the file.** Removal calls the same sign-out
path as `postflight auth logout`, which POSTs the refresh token to the
issuer's logout endpoint before deleting anything. Deleting `credentials.json`
alone ends nothing — the session lives at Keycloak until it idles out, so the
next sign-in would be waved through against a session the user believed they
had closed. A revocation that cannot be delivered is reported and the local
copy is removed anyway; leaving a token on disk because the network was down
is the worse failure.

**It refuses what it does not own, and says so.** Each path is matched against
the package managers first and only then against our own receipt: the two can
disagree only when a manager has installed over a path the installer once
owned, and in that case the file is theirs now. A manager's command is printed
instead of a removal. Deleting a brew-managed file behind brew's back desyncs
its manifest; deleting an npm package directory leaves a dangling symlink in
`{prefix}/bin` that npm has no command to notice or repair. Anything
unrecognised is declined with `make uninstall` and `rm` as the alternatives.
The credentials are still removed whatever else is found, because they are ours
whoever owns the binary.

Deleting another manager's files would be the wrong kind of thorough, and no
CLI that ships through more than one channel does it. But **silence is the
failure to engineer against** — `kubectl krew upgrade` skips a Homebrew-installed
krew with no message and exit 0, forever, while its own notice keeps
recommending the command that will never work. The receipt's absence was a
perfect signal and it is never read as one. So every copy found is named, with
its owner and its removal command, on the success path as well as the refusal
path.

**One receipt, one owned installation.** The receipt is a single file per
machine, so at most one copy can be *proved* ours; anything else the sweep
finds is reported rather than removed. The exit status is non-zero when a
binary would not go, or when nothing here was ours to remove: the thing that
was asked for did not fully happen.

Detection is in `src/products/postflight-cli/src/scope.rs`, shared with every
other verb that changes an installation, and it works on the **resolved** path:

| Manager | Marker | Why that marker |
| --- | --- | --- |
| Homebrew | a `Cellar` path component | The prefix is unusable as a signal — on an Intel Mac it is `/usr/local`, which is also where `make install` lands. Every keg is `<prefix>/Cellar/<formula>/<version>` and every linked binary resolves through one, so the keg identifies Homebrew on a relocated prefix, a Linuxbrew prefix and a keg-only formula alike. |
| npm, pnpm, bun, Yarn | a `node_modules` component, then the manager's own home (`.bun`, `pnpm`, `.yarn`) | They all produce `node_modules` and take different removal commands. Naming npm for all four sends three of them a command that does nothing. |
| npx | an `_npx` component | Not an installation at all. There is nothing to remove and nothing to upgrade, and npm expires the cache itself — so no command is offered. |
| cargo | under `$CARGO_HOME/bin` | — |

Resolution matters as much as the markers. `current_exe()` lies in two ways
that both land here: macOS returns `_NSGetExecutablePath`, which `dyld(3)` says
"may be a symbolic link and not the real file", so an Intel-Mac Homebrew
install arrives as `/usr/local/bin/postflight` and matches nothing until it is
canonicalised; and Linux returns `<path> (deleted)` for an unlinked binary,
which Rust neither strips nor errors on (rust-lang/rust#69343) and which is
exactly what replacing a binary in place leaves behind for the process that
replaced it. Both are corrected in `scope::running_binary` before anything is
matched. Comparing whole path components rather than substrings is what keeps
somebody's `~/src/homebrew` checkout from reading as a Homebrew prefix.

**Confirmation is required, not assumed.** Without `--yes`, a non-terminal
stdin is refused rather than treated as consent — the same reasoning that
makes `curl … | sh` worth being careful about.

Order matters: credentials go first. If the binary removal then fails the user
has a working CLI and merely signs in again; the reverse leaves a token behind
with nothing left to clear it. Unlinking a running executable is fine on Unix
— the inode outlives the name — so the process finishes its own removal and
still prints its report.

`install.sh --uninstall` is the path for a binary too broken to remove itself,
which is exactly when someone reaches for it. It reads the receipt for the
install path, delegates to `postflight self uninstall --yes` when the binary
runs, and only removes files directly when it does not — saying plainly that
the session was not ended, because a shell script has no good way to do it.

Two things exercise removal on a schedule, and they exercise different halves
of it. The install canary's `uninstall` method drives `postflight self
uninstall` every six hours against the live release: install via the public
installer, assert the refusal without a terminal, assert a second copy ahead
on `PATH` is reported and left alone, remove, then assert the binary, the
receipt and the config directory are all gone by name — asserting the home
directory is empty would pass for a run that installed nothing. The handoff
above it — `install.sh --uninstall` reaching a real binary that then honours
`self uninstall --yes` rather than the installer sweeping up by hand — is
asserted by the edge lane's smoke gate on every run of the build lane.
`installer_test.sh` drives install.sh against a stub, which proves the
invocation and nothing about what happens on the other end of it.

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
since npm versions are immutable. Trusted publishing is registered *per
package*, so all five carry their own registration against
`postflight-cli-publish-npm.yml`, and the lane asserts npm ≥ 11.5.1 before
it tries: older npm cannot publish without a token, and the point of this
lane is that it holds none.

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

The lane's first act is to refuse a crate the manifest marks unpublishable,
because cargo's own refusal names neither the key nor the ceremony behind it.
If that guard ever fires again, the remediation is registry-side: claim the
crate name on crates.io, register a trusted publisher against
`postflight-cli-publish-crates.yml` in this repository (the environment
field left empty — the job declares none, and a scoped registration 403s
the token exchange), then drop `publish = false` from the manifest. The
action behind the token mint, `rust-lang/crates-io-auth-action`, is pinned
in `.github/actions-allowlist.json`, whose enforcing half is a repository
setting outside Git — unapplied, the whole workflow dies as a
`startup_failure` (`docs/dependency-management.md`).

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
half-published. The tap repository and the `guardian-promotions`
App-installation grant on it are declared in the `guardian-github` tofu root
(`runbooks/github-as-code.md`), which is also where a formula that failed to
mirror is re-rendered and PUT by hand.

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

A gate inside the build lane, then three scheduled loops, all labelled
`guardian_component: cli-release-train`.

### In-lane: the edge smoke gate

Between the build and the dedup that decides whether to push,
`postflight-cli-image` exercises what it just built. A failure ends the job
with nothing signed and nothing pushed, so `:edge` stays on the last artifact
that passed.

All four targets get the structural check — object magic plus the machine
word or cputype, at the offset the format puts it — which for the three the
runner cannot execute is the only proof available that zig cross-compiled
what the directory name claims. It is the same table the deep-test runner
reads, and `TestPostflightCliObjectShapeTablesAgree` is what keeps the two
from drifting apart.

The native target is additionally run, twice:

- `version` must exit zero and lead with `postflight version <version>`, the
  version read out of `Cargo.toml`'s `[package]` table. install.sh takes the
  last field of that line as the version it records in the receipt.
- `install.sh --uninstall` must reach the binary. The gate stages the
  installation an install would have left — binary, receipt, credentials —
  runs the installer's removal against it, and fails if the output shows the
  fallback that removes files directly or if any of the three files survives.
  The staged credential carries no refresh token on purpose, so sign-out takes
  its local-only branch and the gate needs no network.

The gate does not sit behind the byte-dedup, because the change most likely to
break the handoff is the one dedup would skip: a commit touching only
`dist/install.sh` rebuilds byte-identical binaries and still ships that
installer to the website and the release assets.

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
| `auth_status_live` | `auth status` on a credential the issuer never minted reports the session ended and removes it |
| `auth_status_offline` | `auth status` against an unreachable issuer errors and leaves the credential in place |
| `auth_logout` | `auth logout` ends the session and removes the credential |

The three session checks run against credentials the job forges, because a real
one needs a browser approval this job cannot give. `auth_status_offline` is what
binds the other two to the network: a `status` that decided locally could not
both report an ended session for a forged credential and error for an
unreachable issuer. `AUTH_ISSUER` names the issuer they point at and may not
drift from the binary's compiled-in default —
`TestDeeptestAuthIssuerMatchesTheCliDefault` holds them together, and
`TestDeeptestRecordsTheAuthSessionChecks` keeps the checks from being dropped
without their alerting. What they cannot prove is that signing out ends the
session *at the issuer*: that needs a session to end, so it is asserted in the
postflight device-flow journey canary ([canaries.md](canaries.md)), which
completes a real approval and then walks the same userinfo → logout → userinfo
sequence the CLI does.

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

The channel advances by departures, not by a wall clock. The promoter ticks
every 15 minutes and departs when three things line up: the newest Freight
that has **soaked** (existed for `SOAK_SECONDS`, 1h) is not the one the last
*successful* promotion shipped, no attempt at it happened inside
`RETRY_SECONDS`, and the last departure is at least `DEPARTURE_SECONDS`
(6h) behind. A departure takes the newest soaked Freight, so commits landing
between departures batch into one hop — and a new commit never delays a
departure, it can only change which Freight boards. Those two properties
are the design: restart-the-soak schemes livelock under steady merge
traffic, and the departure interval is the single churn dial. "Already
shipped" is judged against the last **Succeeded** promotion deliberately —
judging against the stage's `lastPromotion` regardless of phase would read
a gate-failed attempt as done and wedge the channel on it forever.

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
verdict yet — one the runner has not adjudicated by departure time, which
the soak makes rare — matches neither and the step retries until the next
run adjudicates it, bounded by `retry.timeout: 28m`. That budget spans a full runner cadence and
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
| `GuardianCliNightlyPromoterSilent` | warning | no promoter heartbeat for 2h |

Only the install canary pages critical, and deliberately: it is the only
loop watching what a user actually hits.

## Cutting a candidate

A candidate is a pointer move. There is no version to bump and nothing to
rebuild: the bytes exist, they have already soaked a rung down, and they
already report the version they carry.

1. **Pick a digest nightly has released.** rc's lineage assert is a digest
   grep against nightly release bodies, so only an artifact published as a
   nightly can be promoted. That is also what makes the deep-test verdict
   transitive: nightly's Kargo promotion gates on `guardian_cli_deeptest_pass`
   at that exact digest, so a candidate inherits an adjudicated verdict
   instead of needing a fresh one.

2. **Open the pin PR, editing both files in one commit.**

   `src/products/postflight-cli/release/channels.yaml`:

   ```yaml
   channels:
     rc:
       image: ghcr.io/guardian-intelligence/postflight-cli@sha256:<nightly digest>
       version: 0.3.0-pre
   ```

   `src/infrastructure/deployments/guardian/system/release-manifest.yaml`:

   ```yaml
   releases:
     postflight-cli:
       rc:
         image: ghcr.io/guardian-intelligence/postflight-cli@sha256:<same digest>
   ```

   The `version` is whatever the pinned bytes report — read it off the
   nightly release's `Version` line, not off main, which may have bumped
   since that artifact was built. The cutter refuses a pin that claims
   anything else.

   Both files, in one commit, for two reasons. Mechanically,
   `TestReleaseManifestCoversReleaseChannels` fails the PR if a channel pins
   a digest the manifest does not list, or if the manifest lists one no
   channel pins. Substantively, the manifest is what puts the digest under
   the countersigner and the release projector: a channel pin alone would
   publish an artifact to users that carries no Guardian countersignature at
   the public registry, which is the one invariant the publication boundary
   holds. Kargo's nightly promotion template carries the second
   `yaml-update` for exactly this reason; a hand cut has to do it by hand.

3. **Merge.** The cutter publishes `postflight-cli/rc-<YYYYMMDD>` as a
   prerelease, from the same assets and the same signatures the nightly
   carried. Nothing downstream fires.

## Cutting a stable

The stable cut is the only one that changes bytes, so it is the only one that
needs a build. It is three merges: the version bump, the pin, and the bump
back.

1. **Check the soak.** A candidate published within the last `RC_SOAK_DAYS`
   (7) days must exist whose `Source commit` main descends from. That is the
   whole of stable's lineage, and the cutter asserts it — starting a cut
   without one produces a pin that will not publish.

2. **Bump the version to the bare number.** `0.3.0-pre` becomes `0.3.0` in
   `src/products/postflight-cli/Cargo.toml`, in `CARGO_PKG_VERSION` in
   `src/products/postflight-cli/BUILD.bazel`, and in the `postflight` package
   entry of `src/products/postflight-cli/Cargo.lock`, all in one PR: cargo
   builds from the first, the release lane ships the second, and the third is
   what `cargo publish --locked` resolves against, so a stale lock fails the
   crates.io publish after the release is already out.
   `//src/products/postflight-cli:packaging_lockstep_test` holds the first
   two together and to the Makefile's binary name; `aspect tidy` refreshes
   the manifest hashes `MODULE.bazel.lock` carries for the crate, which
   belong in the same commit. Nothing else carries a version to bump — the
   npm package templates hold `0.0.0-dev` and the formula template holds
   `@VERSION@`, both injected at publish time from the tag. Merge.

3. **Let edge rebuild, and read the verdict.** The merge touches the crate,
   so `postflight-cli-image` runs; the version change alters
   `CARGO_PKG_VERSION`, therefore the bytes, therefore the binaries digest,
   so the dedup does not skip and `:edge` moves to a digest that has never
   been on nightly or rc and never will be. The deep-test runner picks it up
   within the hour. Only nightly's Kargo promotion gates on that verdict — a
   hand-authored pin does not — so read it before writing the pin. It is the
   check that would have caught a broken cross-compile before anyone
   published it, and it is the only thing that covers the commits between the
   candidate's build commit and this one.

4. **Open the pin PR**, the same two files in one commit, with the bare
   version:

   ```yaml
   channels:
     stable:
       image: ghcr.io/guardian-intelligence/postflight-cli@sha256:<new edge digest>
       version: 0.3.0
   ```

5. **Merge.** The cutter publishes `postflight-cli/v0.3.0` as `--latest`.

6. **Watch what publishing sets off.** The release fires
   `postflight-cli-publish-npm` and `postflight-cli-publish-crates`, and the
   cutter's own stable path renders the Homebrew formula and mirrors it to
   the tap. They are the first thing users touch, and none of them is covered
   by the install canary, whose methods are the release assets and the curl
   installer.

7. **Bump main to the next `-pre` immediately.** A bare `0.3.0` on main is a
   loaded gun: the next commit that changes the binary produces edge bytes
   that report `0.3.0` and are not the `0.3.0` anyone can download, and the
   next nightly hands that to users. The follow-up PR to `0.4.0-pre` is part
   of the cut, not a chore after it.

### What the cutter asserts

`postflight-cli-release` runs on any main push touching `channels.yaml`. Per
newly-pinned channel, in order:

- the pin parses and names a known channel;
- the digest has not already been released on this channel;
- **lineage.** For rc: the digest must already appear in a nightly release
  body — the same bytes, one rung down. For stable: some candidate published
  inside the soak window must record a `Source commit` for which
  `compare(<that commit>...<build commit>)` is `ahead` or `identical`;
- the artifact's `cosign verify` and all four `verify-blob`s pass;
- **version match.** The pinned native binary's `postflight version …` output
  must equal the `version` in the pin, and for stable that version must be
  bare. The pin is what the tag, the release notes, the source tarball name
  and the Homebrew formula all take their version from, so a pin claiming a
  version the bytes do not carry cannot mint a misnamed release.

It then stages assets, mints a short-lived `guardian-promotions` App token,
creates the tag ref, and cuts the release. The App token is minted *after*
the dedup check and is not `GITHUB_TOKEN` on purpose: App-authored releases
fire `release:` workflows, which is what the npm and crates.io lanes ride.
The tag ref is created before the release because the releases API 403s
integration tokens when `target_commitish` is a bare SHA (cli/cli#9514).

Release bodies are read back by machine, which is why their fields are
stable: the install canary greps a body for the pinned digest, a stable cut
walks the candidates' `Source commit` lines out of theirs, and the author of
the next pin reads its `version` off the `Version` line.

### Why stable's lineage is a soak, not a digest match

An rc promotes nightly's exact bytes, so "has this digest been released on
nightly" is a real question with a real answer, and rc's lineage is a digest
grep — one that a same-bytes ladder makes load-bearing rather than
ceremonial.

Stable cannot work that way, and no amount of care makes it able to. The cut
*is* a version change; a version change changes `CARGO_PKG_VERSION`, hence
the bytes, hence the binaries digest, hence the OCI digest. **Stable can
never share a digest with the candidate it promotes.** Any rule that asks for
digest equality across the cut — a symmetric lineage grep, an assert that the
stable artifact was released on rc, a canary that expects one digest on both
channels — is not strict, it is unsatisfiable: no pair of inputs passes it,
so it cannot be met, only removed. Every assert written about a stable cut
has to be checked against that.

So stable asserts the two things that stay checkable across a byte change:

- **the version is bare and the bytes carry it.** The pin says `0.3.0` and
  the artifact says `postflight version 0.3.0`.
- **the soak.** Some candidate whose `Source commit` this build descends from
  was published within the last seven days.

What that buys, exactly, is worth stating flatly, because it is less than the
word "soak" suggests. The ancestry check proves the candidate is *behind* the
stable build rather than beside it — not a fork, not a revert. The window
proves the candidate was *published* recently. It does not bound how much
main history separates the two: publication recency is a property of the
release, not of the commit it was built from, so a candidate published
yesterday from a commit three weeks old satisfies the window exactly as well
as one published yesterday from a commit an hour old.

Everything in that gap — every commit between the candidate's build commit
and the stable's — reaches users having never ridden a candidate. What covers
it is not the soak but the deep-test verdict on the stable artifact's own
digest, read by hand before the pin is written (step 3 above). Narrowing
`RC_SOAK_DAYS` shortens how long a cut may be prepared; it does not shorten
that gap, and nothing in the cutter does.

## Known gaps

**No automated macOS execution coverage.** Both darwin targets are
cross-compiled on a Linux runner, released, packed into npm platform
packages, pinned by the Homebrew formula, and verified at every step. One
`aarch64-apple-darwin` binary has now been run by hand — installed on an
Apple Silicon machine through the public installer on 2026-07-26, signature
verified, `version` correct — which establishes that the lane produces
working binaries and that two undocumented properties currently hold:
`zig cc` emits the ad-hoc Mach-O signature arm64 macOS requires to exec at
all, and `curl | sh` sets no `com.apple.quarantine` attribute, so Gatekeeper
never adjudicates. Neither is asserted anywhere, and the first would break
silently on a toolchain change. Nothing runs a darwin binary on a schedule.
The edge smoke gate and the deep-test runner check Mach-O magic plus the
cputype word; the install canary checks magic only; every version assert in
the estate runs the linux-x64 binary because it is the only one any of them
can execute. A cross-compile that produced a correct-looking Mach-O that faults
on first instruction would pass every gate we have, and the formula's own
`test do` block would be the first thing to notice — on a user's machine.
The honest closer is a satellite canary on real Apple hardware reporting
into the same series; the `platform` label on
`guardian_cli_install_canary_success` exists for it, and the `cli.` event
prefix is already registered so a satellite can speak the same vocabulary
through the public path.

**Nothing canaries the ecosystem mirrors.** The install canary's methods are
the release assets and the curl installer. A published npm version that
resolves nothing to run, a crate that stopped building on a fresh toolchain,
or a tap formula whose checksums drifted would be found by a user, not by us.
All three now hold a published version, so this is the next method to add.

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
