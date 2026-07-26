# Postflight CLI

`postflight` is the command-line front door for [Postflight](https://guardianintelligence.org/postflight) —
run GitHub CI on warm, isolated infrastructure.

Today it signs you in and manages its own install. Job control ships next.

## Install

### Installer script (macOS and Linux)

```sh
curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh
```

Installs the latest stable release to `~/.local/bin`. Re-run it to upgrade.
Pass `-s -- --channel rc` or `--channel nightly` for prereleases, `--uninstall` to remove.

### Homebrew

```sh
brew install guardian-intelligence/tap/postflight
```

### npm

```sh
npm install -g @guardian-intelligence/postflight
```

### Cargo

```sh
cargo install postflight    # or: cargo binstall postflight
```

### Direct download

Grab `postflight-<target>` from the [latest release](https://github.com/guardian-intelligence/guardian/releases),
verify it, and put it on your `PATH`:

```sh
cosign verify-blob postflight-x86_64-unknown-linux-musl \
  --bundle postflight-x86_64-unknown-linux-musl.sigstore.json \
  --certificate-identity 'https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
chmod +x postflight-x86_64-unknown-linux-musl
```

Builds ship for Linux (x86_64, aarch64, static musl) and macOS (Intel, Apple silicon).
Channels, tags, and provenance are documented in
[docs/postflight-cli-distribution.md](../../../docs/postflight-cli-distribution.md).

### From source

```sh
cd src/products/postflight-cli
make && sudo make install
```

## Quickstart

```sh
postflight auth login     # prints a code and an approval URL — open it, approve, done
postflight auth status    # who you're signed in as
postflight version        # version, channel, and how this copy was installed
```

First sign-in creates your account; there is no separate signup.
Credentials are stored with mode 0600 under `~/.config/postflight/`.

## Uninstall

```sh
postflight self uninstall
```

Removes the binary, credentials, and install receipt, and ends your session at the issuer.
If a package manager owns this install, it declines and prints that manager's uninstall command instead.

## License

[Apache-2.0](LICENSE)
