# postflight CLI

The `postflight` binary — the product's front door. Verbs follow `gh`
conventions; there is no `signup` verb (first sign-in auto-creates the
account via the broker).

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
