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
also when the remaining methods land. The channel ladder, the signing
contract and the cut ceremony are in
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
