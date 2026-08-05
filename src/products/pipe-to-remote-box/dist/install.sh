#!/bin/sh
# Installer for Pipe to Remote Box. A release also carries this file and a
# Sigstore bundle for verifying it before execution.
#
# Nothing with side effects runs until the braced call on the final line. A
# truncated download therefore reaches EOF inside an incomplete compound
# command and is rejected without installing anything.
set -eu

REPO="guardian-intelligence/guardian"
RELEASES_URL="https://api.github.com/repos/$REPO/releases"
DOWNLOAD_URL="https://github.com/$REPO/releases/download"
RELEASES_PER_PAGE=100
RELEASES_MAX_PAGES=10
TAG_PREFIX="pipe-to-remote-box"
BUILD_IDENTITY="https://github.com/guardian-intelligence/guardian/.github/workflows/pipe-to-remote-box-image.yml@refs/heads/main"
OIDC_ISSUER="https://token.actions.githubusercontent.com"
CHANNELS="stable rc nightly"

log() {
  printf 'pipe-to-remote-box: %s\n' "$1" >&2
}

fail() {
  printf 'pipe-to-remote-box: %s\n' "$1" >&2
  exit 1
}

usage() {
  cat <<'EOF'
usage: install.sh [--channel <stable|rc|nightly> | --version <x.y.z>]

options:
  --channel <channel>  release channel (default: stable)
  --version <x.y.z>    install one exact stable version
  --print-tag          resolve and print the release tag without installing
  -h, --help           print this message

environment:
  PIPE_TO_REMOTE_BOX_CHANNEL      same as --channel
  PIPE_TO_REMOTE_BOX_INSTALL_DIR  destination (default: ~/.local/bin)
EOF
}

cleanup() {
  if [ -n "$work" ]; then
    rm -rf "$work"
  fi
  if [ -n "$staged" ]; then
    rm -f "$staged"
  fi
}

fetch() {
  curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    fail "neither sha256sum nor shasum is on PATH; cannot verify the download"
  fi
}

# Tests can replace only the release-listing transport with local pages. Asset
# downloads still go through curl and still pass checksum and signature gates.
releases_page() {
  if [ -n "${PIPE_TO_REMOTE_BOX_RELEASES_DIR:-}" ]; then
    if [ -f "$PIPE_TO_REMOTE_BOX_RELEASES_DIR/page-$1.json" ]; then
      cat "$PIPE_TO_REMOTE_BOX_RELEASES_DIR/page-$1.json" >"$2"
    else
      printf '[]\n' >"$2"
    fi
    return 0
  fi
  fetch "$RELEASES_URL?per_page=$RELEASES_PER_PAGE&page=$1" "$2"
}

load_page() {
  page_no="$1"
  [ "$page_no" -le "$RELEASES_MAX_PAGES" ] || return 1
  if [ "$last_page" -ne 0 ] && [ "$page_no" -gt "$last_page" ]; then
    return 1
  fi
  page_fields="$work/fields.$page_no"
  if [ -f "$page_fields" ]; then
    return 0
  fi

  page_json="$work/releases.$page_no.json"
  releases_page "$page_no" "$page_json" \
    || fail "could not list releases from $RELEASES_URL"
  tr ',' '\n' <"$page_json" \
    | sed -n \
      -e 's/^[[{[:space:]]*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/tag \1/p' \
      -e 's/^[[{[:space:]]*"prerelease"[[:space:]]*:[[:space:]]*true.*/pre true/p' \
      -e 's/^[[{[:space:]]*"prerelease"[[:space:]]*:[[:space:]]*false.*/pre false/p' \
      >"$page_fields"

  page_tags="$(grep -c '^tag ' "$page_fields" || true)"
  if [ "$page_tags" -eq 0 ]; then
    rm -f "$page_fields"
    [ "$page_no" -gt 1 ] || fail "could not read any release from $RELEASES_URL"
    last_page=$((page_no - 1))
    return 1
  fi
  if [ "$page_tags" -lt "$RELEASES_PER_PAGE" ]; then
    last_page="$page_no"
  fi
}

matches_channel() {
  case "$1" in
    nightly)
      [ "$3" = true ] \
        && printf '%s\n' "$2" \
          | grep -Eq "^$TAG_PREFIX/nightly-[0-9]{8}(-[0-9]{4})?$" \
        && return 0
      ;;
    rc)
      [ "$3" = true ] \
        && printf '%s\n' "$2" \
          | grep -Eq "^$TAG_PREFIX/rc-[0-9]{8}(-[0-9]{4})?$" \
        && return 0
      ;;
    stable)
      [ "$3" = false ] \
        && printf '%s\n' "$2" \
          | grep -Eq "^$TAG_PREFIX/v[0-9]+\.[0-9]+\.[0-9]+$" \
        && return 0
      ;;
  esac
  return 1
}

scan_page() {
  scan_channel="$1"
  scan_tag=""
  while IFS= read -r field || [ -n "$field" ]; do
    case "$field" in
      "tag "*) scan_tag="${field#tag }" ;;
      "pre "*)
        if [ -n "$scan_tag" ] \
          && matches_channel "$scan_channel" "$scan_tag" "${field#pre }"; then
          printf '%s\n' "$scan_tag"
          return 0
        fi
        scan_tag=""
        ;;
    esac
  done <"$2"
  return 1
}

resolve_tag() {
  resolve_page=1
  while load_page "$resolve_page"; do
    if scan_page "$1" "$work/fields.$resolve_page"; then
      return 0
    fi
    resolve_page=$((resolve_page + 1))
  done
  return 1
}

main() {
  channel="${PIPE_TO_REMOTE_BOX_CHANNEL:-stable}"
  channel_given=no
  pinned_version=""
  install_dir="${PIPE_TO_REMOTE_BOX_INSTALL_DIR:-}"
  print_tag=no
  last_page=0
  staged=""
  work=""

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --channel)
        [ "$#" -ge 2 ] || fail "--channel needs a value"
        channel="$2"
        channel_given=yes
        shift 2
        ;;
      --channel=*)
        channel="${1#--channel=}"
        channel_given=yes
        shift
        ;;
      --version)
        [ "$#" -ge 2 ] || fail "--version needs a value"
        pinned_version="$2"
        shift 2
        ;;
      --version=*)
        pinned_version="${1#--version=}"
        shift
        ;;
      --print-tag)
        print_tag=yes
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

  for tool in curl grep sed tr; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required but not on PATH"
  done

  if [ -n "$pinned_version" ]; then
    [ "$channel_given" = no ] \
      || fail "--version and --channel are alternatives"
    printf '%s\n' "$pinned_version" \
      | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
      || fail "--version takes a bare stable version such as 1.2.3"
  else
    case " $CHANNELS " in
      *" $channel "*) ;;
      *) fail "unknown channel '$channel' (choose one of: $CHANNELS)" ;;
    esac
  fi

  if [ -z "$install_dir" ]; then
    [ -n "${HOME:-}" ] \
      || fail "HOME is unset; set PIPE_TO_REMOTE_BOX_INSTALL_DIR"
    install_dir="$HOME/.local/bin"
  fi
  case "$install_dir" in
    /*) ;;
    *) fail "PIPE_TO_REMOTE_BOX_INSTALL_DIR must be an absolute path" ;;
  esac

  if [ -n "${SUDO_USER:-}" ] \
    && [ -z "${PIPE_TO_REMOTE_BOX_INSTALL_DIR:-}" ]; then
    fail "an implicit install under sudo would use root's home; rerun without sudo or set PIPE_TO_REMOTE_BOX_INSTALL_DIR explicitly"
  fi

  work="$(mktemp -d "${TMPDIR:-/tmp}/pipe-to-remote-box-install.XXXXXX")"
  trap cleanup EXIT INT TERM

  if [ -n "$pinned_version" ]; then
    tag="$TAG_PREFIX/v$pinned_version"
  elif resolve_tag "$channel" >"$work/tag"; then
    tag="$(cat "$work/tag")"
  else
    available=""
    for candidate in $CHANNELS; do
      if resolve_tag "$candidate" >/dev/null; then
        available="$available $candidate"
      fi
    done
    [ -n "$available" ] \
      || fail "no Pipe to Remote Box release exists on any channel"
    fail "no release exists on '$channel'; available channels:${available}"
  fi

  if [ "$print_tag" = yes ]; then
    printf '%s\n' "$tag"
    exit 0
  fi

  command -v cosign >/dev/null 2>&1 \
    || fail "cosign is required to verify the release signature before installation (https://docs.sigstore.dev/cosign/installation)"

  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os:$arch" in
    Linux:x86_64 | Linux:amd64) target="x86_64-unknown-linux-musl" ;;
    Linux:aarch64 | Linux:arm64) target="aarch64-unknown-linux-musl" ;;
    Darwin:x86_64) target="x86_64-apple-darwin" ;;
    Darwin:arm64 | Darwin:aarch64) target="aarch64-apple-darwin" ;;
    *) fail "unsupported platform $os/$arch" ;;
  esac

  log "installing $tag ($target)"
  tag_path="$(printf '%s' "$tag" | sed 's|/|%2F|g')"
  asset="pipe-to-remote-box-$target"
  base="$DOWNLOAD_URL/$tag_path"

  fetch "$base/$asset" "$work/$asset" \
    || fail "$tag carries no $asset asset"
  fetch "$base/checksums.txt" "$work/checksums.txt" \
    || fail "$tag carries no checksums.txt asset"

  expected="$(grep "^[0-9a-f]\{64\}  $asset\$" "$work/checksums.txt" | cut -d' ' -f1)"
  [ -n "$expected" ] || fail "checksums.txt carries no entry for $asset"
  actual="$(sha256_of "$work/$asset")"
  [ "$actual" = "$expected" ] \
    || fail "sha256 mismatch for $asset (expected $expected, got $actual)"

  fetch "$base/$asset.sigstore.json" "$work/$asset.sigstore.json" \
    || fail "$tag carries no Sigstore bundle for $asset"
  if ! cosign_output="$(cosign verify-blob \
    --bundle "$work/$asset.sigstore.json" \
    --certificate-identity "$BUILD_IDENTITY" \
    --certificate-oidc-issuer "$OIDC_ISSUER" \
    "$work/$asset" 2>&1)"; then
    printf '%s\n' "$cosign_output" >&2
    fail "signature verification failed for $asset"
  fi
  log "signature verified against $BUILD_IDENTITY"

  mkdir -p "$install_dir" || fail "could not create $install_dir"
  chmod 0755 "$work/$asset"
  staged="$install_dir/.pipe-to-remote-box.install.$$"
  mv "$work/$asset" "$staged" || fail "could not stage the binary in $install_dir"

  if ! version_output="$("$staged" --version 2>&1)"; then
    printf '%s\n' "$version_output" >&2
    fail "the verified $target binary does not run on this machine"
  fi
  case "$version_output" in
    "pipe-to-remote-box "*) ;;
    *) fail "the verified asset returned an unexpected version response" ;;
  esac

  if [ -n "$pinned_version" ] \
    && [ "$version_output" != "pipe-to-remote-box $pinned_version" ]; then
    fail "the verified asset reports '$version_output', not requested version $pinned_version"
  fi

  mv "$staged" "$install_dir/pipe-to-remote-box" \
    || fail "could not install $install_dir/pipe-to-remote-box"
  staged=""
  log "installed $version_output to $install_dir/pipe-to-remote-box"

  case ":${PATH:-}:" in
    *":$install_dir:"*) ;;
    *) log "$install_dir is not on PATH; add it with: export PATH=\"$install_dir:\$PATH\"" ;;
  esac
}

# A complete brace group is parsed before its body runs. If the network cuts
# this script at any proper prefix, the group is incomplete and nothing inside
# it executes.
{ main "$@"; }
