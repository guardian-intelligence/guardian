#!/bin/sh
# Installer for the postflight CLI, served at
# https://guardianintelligence.org/postflight/install.sh and meant to be run
# as `curl -fsSL <url> | sh`. It resolves the newest GitHub Release on a
# channel, verifies what it downloaded, and drops the binary in ~/.local/bin
# so no step of the happy path needs sudo.
#
#   curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh
#   curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh -s -- --channel nightly
#
# Dependencies are deliberately limited to what a bare container already has:
# curl, sed/grep, and either sha256sum (Linux) or shasum (macOS). No jq — the
# releases JSON is parsed with sed, anchored so that key names appearing
# inside release-note strings (always backslash-escaped in JSON) cannot match.
set -eu

REPO="guardian-intelligence/guardian"
RELEASES_URL="https://api.github.com/repos/$REPO/releases?per_page=100"
DOWNLOAD_URL="https://github.com/$REPO/releases/download"
TAG_PREFIX="postflight-cli"
BUILD_IDENTITY="https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main"
ISSUER="https://token.actions.githubusercontent.com"
CHANNELS="stable rc nightly"

channel="${POSTFLIGHT_CHANNEL:-stable}"
install_dir="${POSTFLIGHT_INSTALL_DIR:-}"
require_verification=no

log() {
  printf 'postflight: %s\n' "$1" >&2
}

fail() {
  printf 'postflight: %s\n' "$1" >&2
  exit 1
}

usage() {
  cat <<'EOF'
usage: install.sh [--channel <stable|rc|nightly>] [--require-verification]

options:
  --channel <ch>          release channel to install from (default: stable)
  --require-verification  fail unless cosign is available to check the
                          signature; without it a missing cosign only warns
  -h, --help              print this message

environment:
  POSTFLIGHT_CHANNEL      same as --channel
  POSTFLIGHT_INSTALL_DIR  install destination (default: ~/.local/bin)
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --channel)
      [ "$#" -ge 2 ] || fail "--channel needs a value"
      channel="$2"
      shift 2
      ;;
    --channel=*)
      channel="${1#--channel=}"
      shift
      ;;
    --require-verification)
      require_verification=yes
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      fail "unknown argument: $1"
      ;;
  esac
done

case " $CHANNELS " in
  *" $channel "*) ;;
  *) fail "unknown channel '$channel' (choose one of: $CHANNELS)" ;;
esac

if [ -z "$install_dir" ]; then
  [ -n "${HOME:-}" ] || fail "HOME is unset — set POSTFLIGHT_INSTALL_DIR to choose a destination"
  install_dir="$HOME/.local/bin"
fi

for tool in curl sed grep; do
  command -v "$tool" > /dev/null 2>&1 || fail "$tool is required but not on PATH"
done

if command -v cosign > /dev/null 2>&1; then
  verify_signature=yes
elif [ "$require_verification" = yes ]; then
  fail "--require-verification was given but cosign is not on PATH (https://docs.sigstore.dev/cosign/installation)"
else
  verify_signature=no
fi

sha256_of() {
  if command -v sha256sum > /dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum > /dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    fail "neither sha256sum nor shasum is on PATH — cannot verify the download"
  fi
}

os="$(uname -s)"
arch="$(uname -m)"
case "$os:$arch" in
  Linux:x86_64 | Linux:amd64) target="x86_64-unknown-linux-musl" ;;
  Linux:aarch64 | Linux:arm64) target="aarch64-unknown-linux-musl" ;;
  Darwin:x86_64) target="x86_64-apple-darwin" ;;
  Darwin:arm64 | Darwin:aarch64) target="aarch64-apple-darwin" ;;
  *)
    fail "unsupported platform $os/$arch — released targets are:
  x86_64-unknown-linux-musl
  aarch64-unknown-linux-musl
  x86_64-apple-darwin
  aarch64-apple-darwin"
    ;;
esac

# BSD mktemp (macOS) rejects a bare -d, so the template is not optional.
work="$(mktemp -d "${TMPDIR:-/tmp}/postflight-install.XXXXXX")"
trap 'rm -rf "$work"' EXIT INT TERM

fetch() {
  curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1"
}

# /releases/latest is unusable here: every release so far is a prerelease, and
# the repository also carries unrelated data drops, so the channel's newest
# release is whichever listed release matches its tag shape first (the API
# returns releases newest-first).
if ! fetch "$RELEASES_URL" "$work/releases.json"; then
  fail "could not list releases from $RELEASES_URL (GitHub rate-limits unauthenticated callers to 60 requests per hour per IP)"
fi

tr ',' '\n' < "$work/releases.json" \
  | sed -n \
    -e 's/^[[{[:space:]]*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/tag \1/p' \
    -e 's/^[[{[:space:]]*"prerelease"[[:space:]]*:[[:space:]]*true.*/pre true/p' \
    -e 's/^[[{[:space:]]*"prerelease"[[:space:]]*:[[:space:]]*false.*/pre false/p' \
    > "$work/fields"

[ -s "$work/fields" ] || fail "could not read any release from $RELEASES_URL"

matches_channel() {
  case "$1" in
    nightly)
      case "$2" in
        "$TAG_PREFIX/nightly-"*) return 0 ;;
      esac
      ;;
    rc)
      case "$2" in
        "$TAG_PREFIX/v"*"-rc."*) return 0 ;;
      esac
      ;;
    stable)
      case "$2" in
        "$TAG_PREFIX/v"*) [ "$3" = "false" ] && return 0 ;;
      esac
      ;;
  esac
  return 1
}

resolve_tag() {
  resolve_channel="$1"
  resolve_tag_name=""
  field=""
  while IFS= read -r field || [ -n "$field" ]; do
    case "$field" in
      "tag "*)
        resolve_tag_name="${field#tag }"
        ;;
      "pre "*)
        if [ -n "$resolve_tag_name" ] \
          && matches_channel "$resolve_channel" "$resolve_tag_name" "${field#pre }"; then
          printf '%s\n' "$resolve_tag_name"
          return 0
        fi
        resolve_tag_name=""
        ;;
    esac
  done < "$work/fields"
  return 1
}

if ! tag="$(resolve_tag "$channel")"; then
  available=""
  for candidate in $CHANNELS; do
    if resolve_tag "$candidate" > /dev/null; then
      available="$available $candidate"
    fi
  done
  [ -n "$available" ] || fail "no postflight CLI release exists on any channel yet"
  fail "no release on the '$channel' channel yet. Available channels:$available
Install from one explicitly, for example:

  curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh -s -- --channel nightly"
fi

log "installing $tag ($target)"

# Release-asset URLs percent-encode the slash in the tag; a literal slash
# resolves only through a redirect that not every proxy follows.
tag_path="$(printf '%s' "$tag" | sed 's|/|%2F|g')"
asset="postflight-$target"

fetch "$DOWNLOAD_URL/$tag_path/$asset" "$work/$asset" \
  || fail "$tag has no $asset asset"
fetch "$DOWNLOAD_URL/$tag_path/checksums.txt" "$work/checksums.txt" \
  || fail "$tag has no checksums.txt asset"

expected="$(grep "^[0-9a-f]\{64\}  $asset\$" "$work/checksums.txt" | cut -d' ' -f1)"
[ -n "$expected" ] || fail "checksums.txt carries no entry for $asset"
actual="$(sha256_of "$work/$asset")"
[ "$actual" = "$expected" ] || fail "sha256 mismatch for $asset (expected $expected, got $actual)"

if [ "$verify_signature" = yes ]; then
  fetch "$DOWNLOAD_URL/$tag_path/$asset.sigstore.json" "$work/$asset.sigstore.json" \
    || fail "$tag has no signature bundle for $asset"
  cosign verify-blob \
    --bundle "$work/$asset.sigstore.json" \
    --certificate-identity "$BUILD_IDENTITY" \
    --certificate-oidc-issuer "$ISSUER" \
    "$work/$asset" > /dev/null \
    || fail "signature verification failed for $asset"
  log "signature verified against $BUILD_IDENTITY"
else
  log "cosign is not on PATH — checksum verified, signature not. Install cosign and rerun with --require-verification for the full chain."
fi

mkdir -p "$install_dir" || fail "could not create $install_dir"
chmod 0755 "$work/$asset"
mv "$work/$asset" "$install_dir/postflight" || fail "could not write $install_dir/postflight"

version="$("$install_dir/postflight" version)" || fail "$install_dir/postflight was installed but does not run"
log "installed $version to $install_dir/postflight"

case ":${PATH:-}:" in
  *":$install_dir:"*) ;;
  *) log "$install_dir is not on your PATH — add it with: export PATH=\"$install_dir:\$PATH\"" ;;
esac
