# postflight CLI

The `postflight` binary — the product's front door. Verbs follow `gh`
conventions; there is no `signup` verb (first sign-in auto-creates the
account via the broker).

## Install

| Method | Command | Channels | Status |
|---|---|---|---|
| make | `make && sudo make install` in this directory of a monorepo clone | all | live |
| raw | download the Release asset for your target, verify it, run it | all | live |
| curl | `curl -fsSL https://guardianintelligence.org/postflight/install.sh \| sh` | stable, `--channel nightly\|rc` | live |
| cargo | `cargo install postflight` | stable | planned |
| cargo-binstall | `cargo binstall postflight` | stable | planned |
| npm/bun | `npm i -g @guardian-intelligence/postflight` | stable | planned |
| brew | `brew install guardian-intelligence/tap/postflight` | stable | planned |
| mise | tool `ubi:guardian-intelligence/guardian` with `exe=postflight` and `tag_regex=^postflight-cli/v` | stable | planned |

Releases so far are nightlies (`postflight-cli/nightly-YYYYMMDD`), so the
curl installer needs `--channel nightly` until the first stable cut, which is
also when the remaining methods land. Pass `--version <x.y.z>` instead of a
channel to pin one release, and `--require-verification` to fail rather than
warn when cosign is missing.

Every release also carries `install.sh` and a signature bundle for it, so the
installer can be verified before it is run instead of piped in from the
website. That recipe, the channel ladder, the signing contract and the cut
ceremony are in
[docs/postflight-cli-distribution.md](../../../docs/postflight-cli-distribution.md).

The `make` path needs cargo — it drives `cargo build --release --locked` and
installs the binary into `$(DESTDIR)$(PREFIX)/bin` (`PREFIX` defaults to
`/usr/local`, `DESTDIR` stages for packagers). GNU make is the baseline. Build
and install separately so the compile never runs under `sudo`:

```sh
make
sudo make install            # or: make install PREFIX="$HOME/.local"
```

`make check` runs the crate's tests, `sudo make uninstall` removes the
installed binary (drop the `sudo` and pass the same `PREFIX` if you installed
under `$HOME`), `make clean` drops the cargo output directory.

## Uninstall

```sh
postflight self uninstall
```

This removes the binary, the stored credentials, and the install receipt, and
ends the session at the sign-in service on the way out — deleting the
credentials file alone leaves that session open until it idles out. Pass
`--keep-credentials` to stay signed in, or `--yes` to skip the confirmation;
without a terminal to answer it, confirmation is required rather than assumed.

A copy installed by cargo, Homebrew, or npm is left alone and the right
command is printed instead: removing it here would leave the package manager
believing it is still installed. Same for a copy with no install receipt —
`make uninstall` handles a source install, and anything else is a `rm`.

When the binary is too broken to run, the installer does it:

```sh
curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh -s -- --uninstall
```

That path removes the same files but cannot end the session, which then
expires on its own.

## Provenance

The binary carries its crate version and nothing else — identical sources
rebuild byte-identically, which is what lets one signed artifact ride every
channel from edge to stable. Which release a copy came from is therefore
recorded beside it, in `~/.config/postflight/install-receipt.json`, by
whatever installed it:

```
$ postflight version
postflight version 0.2.0-nightly
  release   postflight-cli/nightly-20260726
  channel   nightly
  installed 2026-07-26T05:24:16Z via install.sh
```

A receipt is evidence about the one path it names, so a `cargo install` copy
sitting alongside an installer-managed one does not borrow its provenance and
prints the version alone. `postflight version --short` prints the bare version
for scripts.

Release assets are per target (`postflight-x86_64-unknown-linux-musl`,
`aarch64-unknown-linux-musl`, and both darwin triples) and ship a sigstore
bundle each plus a `checksums.txt`. Verify before running:

```sh
cosign verify-blob --bundle postflight-x86_64-unknown-linux-musl.sigstore.json \
  --certificate-identity https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  postflight-x86_64-unknown-linux-musl
```

## Development

Rust in this repository holds a deliberately high bar:

- latest stable toolchain, pinned in `rust.MODULE.bazel`
- `#![forbid(unsafe_code)]` at every crate root
- clippy's pedantic tier gates the build (`.bazelrc` sets `-Dwarnings`)
- pure-Rust dependencies only: rustls for TLS, no C linkage
- `rustfmt` runs via `aspect tidy` (the `:format` target)

`Cargo.toml`/`Cargo.lock` drive crate resolution (Renovate proposes bumps
through the standard cargo manager); Bazel consumes them through
`crate_universe`, so `bazelisk build //src/products/postflight-cli/...`
is the same graph CI gates.

Sign-in uses the OAuth device grant against the `guardianintelligence.org`
realm. The CLI prints the product's own approval page
(`/postflight/device`), never the issuer's verification URI — that page is
where device-flow policy (phishing context, per-user opt-out) lives.

`auth login` writes `credentials.json` (mode 0600) under
`~/.config/postflight`, recording the issuer and client that minted the tokens
so every later command asks the same server. Neither session verb trusts that
file on its own:

- **`auth status`** asks the issuer who the stored token belongs to. A rejected
  access token is retried once behind a refresh and the rotated set is written
  back, so a session stays signed in across the access token's lifetime without
  anyone re-approving. Credentials the issuer has disowned are removed and
  reported as an ended session. Failing to *reach* the issuer is an error that
  leaves the credentials alone — unreachable is not signed out.
- **`auth logout`** ends the session at the issuer before removing the local
  credentials, and removes them whatever the answer: nobody who asked to sign
  out should be left holding a usable token because the network was down.
