#!/bin/sh
# Installer for the postflight CLI. Every release also carries this script and
# a signature bundle for it; docs/postflight-cli-distribution.md has the recipe
# for verifying it before running it.
#
#   curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh
#   curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh -s -- --channel nightly
#
# Nothing runs until `main "$@"` on the last line, so a download truncated in
# flight dies on an unterminated function body having executed nothing.
set -eu

REPO="guardian-intelligence/guardian"
RELEASES_URL="https://api.github.com/repos/$REPO/releases"
RELEASES_PER_PAGE=100
RELEASES_MAX_PAGES=10
DOWNLOAD_URL="https://github.com/$REPO/releases/download"
TAG_PREFIX="postflight-cli"
BUILD_IDENTITY="https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main"
ISSUER="https://token.actions.githubusercontent.com"
CHANNELS="stable rc nightly"

log() {
  printf 'postflight: %s\n' "$1" >&2
}

fail() {
  printf 'postflight: %s\n' "$1" >&2
  exit 1
}

usage() {
  cat <<'EOF'
usage: install.sh [--channel <stable|rc|nightly> | --version <x.y.z>]
                  [--require-verification]
       install.sh --uninstall

options:
  --channel <ch>          release channel to install from (default: stable)
  --version <x.y.z>       install exactly this version instead of whatever a
                          channel currently points at
  --require-verification  fail unless cosign is available to check the
                          signature; without it a missing cosign only warns
  --print-tag             print the release tag that would be installed and
                          exit without installing anything
  --uninstall             remove an installation this script created. Prefer
                          `postflight self uninstall`; this is the path for
                          when the binary is too broken to remove itself
  -h, --help              print this message

environment:
  POSTFLIGHT_CHANNEL      same as --channel
  POSTFLIGHT_INSTALL_DIR  install destination (default: ~/.local/bin)
EOF
}

# Mirrors the CLI's own config_dir: XDG first, ~/.config second, and no
# guessing when neither is set.
config_dir() {
  if [ -n "${XDG_CONFIG_HOME:-}" ]; then
    printf '%s\n' "$XDG_CONFIG_HOME/postflight"
  elif [ -n "${HOME:-}" ]; then
    printf '%s\n' "$HOME/.config/postflight"
  else
    return 1
  fi
}

json_escape() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# The binary carries its crate version and nothing else — that is what lets one
# signed artifact ride every channel — so the release tag, the channel and the
# install method are recorded beside it. `postflight version` reads this back,
# and `postflight self uninstall` uses it to know the file is its to remove.
write_receipt() { # <binary> <channel> <tag> <version> <target> <sha256>
  receipt_dir=""
  if ! receipt_dir="$(config_dir)"; then
    log "neither XDG_CONFIG_HOME nor HOME is set — no install receipt written, so \`postflight version\` will not know which release this is"
    return 0
  fi
  if ! mkdir -p "$receipt_dir" 2> /dev/null; then
    log "could not create $receipt_dir — no install receipt written"
    return 0
  fi
  # A receipt is provenance, not the install: failing to write one must not
  # fail an install that already succeeded.
  cat > "$receipt_dir/install-receipt.json" 2> /dev/null <<EOF || log "could not write $receipt_dir/install-receipt.json"
{
  "schema": 1,
  "method": "install.sh",
  "binary_path": "$(json_escape "$1")",
  "channel": "$(json_escape "$2")",
  "tag": "$(json_escape "$3")",
  "version": "$(json_escape "$4")",
  "target": "$(json_escape "$5")",
  "binary_sha256": "$(json_escape "$6")",
  "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
}

# The capture stops at the first quote rather than the last: a receipt written
# on one line would otherwise hand back the rest of the JSON as a path. An
# install directory containing a literal quote reads as absent and falls back
# to the default, which is the safe way to be wrong.
receipt_binary_path() { # <receipt file>
  sed -n 's/.*"binary_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$1" \
    | sed 's/\\\\/\\/g' \
    | head -n 1
}

# The binary removes itself when it can: that path ends the session at the
# sign-in service, which a shell script has no good way to do. Here we handle
# the case it cannot — a binary that will not run is exactly when someone
# reaches for the installer to get rid of it.
uninstall_installation() {
  uninstall_dir=""
  uninstall_receipt=""
  uninstall_bin="$install_dir/postflight"
  if uninstall_dir="$(config_dir)"; then
    uninstall_receipt="$uninstall_dir/install-receipt.json"
    if [ -f "$uninstall_receipt" ]; then
      recorded="$(receipt_binary_path "$uninstall_receipt")"
      [ -n "$recorded" ] && uninstall_bin="$recorded"
    fi
  fi

  if [ -x "$uninstall_bin" ] && "$uninstall_bin" self uninstall --yes; then
    return 0
  fi

  if [ ! -e "$uninstall_bin" ] && [ ! -f "$uninstall_receipt" ]; then
    fail "nothing to uninstall: no binary at $uninstall_bin and no install receipt"
  fi

  log "removing files directly — the installed binary could not remove itself"
  if [ -e "$uninstall_bin" ]; then
    rm -f "$uninstall_bin" || fail "could not remove $uninstall_bin"
    log "removed $uninstall_bin"
  else
    # Reported rather than shrugged off: a receipt naming a binary that is not
    # there means removal is finishing someone else's job, and staying quiet
    # about it is how a leftover install elsewhere goes unnoticed.
    log "no binary at $uninstall_bin — it was already gone"
  fi
  if [ -n "$uninstall_dir" ]; then
    for leftover in install-receipt.json credentials.json; do
      if [ -e "$uninstall_dir/$leftover" ]; then
        rm -f "$uninstall_dir/$leftover" || fail "could not remove $uninstall_dir/$leftover"
        log "removed $uninstall_dir/$leftover"
      fi
    done
    rmdir "$uninstall_dir" 2> /dev/null || true
  fi
  [ ! -e "$uninstall_bin" ] || fail "$uninstall_bin is still there"
  log "any sign-in session at the service was not ended here — it expires on its own"
}

sha256_of() {
  if command -v sha256sum > /dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum > /dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    fail "neither sha256sum nor shasum is on PATH — cannot verify the download"
  fi
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

# POSTFLIGHT_RELEASES_DIR swaps the API for page-<n>.json fixtures, so channel
# resolution is testable without a network.
releases_page() {
  if [ -n "${POSTFLIGHT_RELEASES_DIR:-}" ]; then
    if [ -f "$POSTFLIGHT_RELEASES_DIR/page-$1.json" ]; then
      cat "$POSTFLIGHT_RELEASES_DIR/page-$1.json" > "$2"
    else
      printf '[]\n' > "$2"
    fi
    return 0
  fi
  fetch "$RELEASES_URL?per_page=$RELEASES_PER_PAGE&page=$1" "$2"
}

# /releases/latest is unusable: prereleases are invisible to it and the
# repository carries unrelated data drops, so a channel's newest release is the
# first listed one matching its tag shape. Pages are fetched one at a time and
# kept, keeping the common case to a single request against GitHub's 60/hour
# unauthenticated budget.
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
    || fail "could not list releases from $RELEASES_URL (GitHub rate-limits unauthenticated callers to 60 requests per hour per IP)"
  tr ',' '\n' < "$page_json" \
    | sed -n \
      -e 's/^[[{[:space:]]*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/tag \1/p' \
      -e 's/^[[{[:space:]]*"prerelease"[[:space:]]*:[[:space:]]*true.*/pre true/p' \
      -e 's/^[[{[:space:]]*"prerelease"[[:space:]]*:[[:space:]]*false.*/pre false/p' \
      > "$page_fields"
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
  return 0
}

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

scan_page() {
  scan_channel="$1"
  scan_tag=""
  field=""
  while IFS= read -r field || [ -n "$field" ]; do
    case "$field" in
      "tag "*)
        scan_tag="${field#tag }"
        ;;
      "pre "*)
        if [ -n "$scan_tag" ] \
          && matches_channel "$scan_channel" "$scan_tag" "${field#pre }"; then
          printf '%s\n' "$scan_tag"
          return 0
        fi
        scan_tag=""
        ;;
    esac
  done < "$2"
  return 1
}

# Callers redirect stdout rather than substituting: a subshell would drop the
# page bookkeeping this leaves behind for the next call.
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
  channel="${POSTFLIGHT_CHANNEL:-stable}"
  channel_given=no
  pinned_version=""
  install_dir="${POSTFLIGHT_INSTALL_DIR:-}"
  require_verification=no
  print_tag=no
  uninstall=no
  last_page=0
  staged=""
  work=""

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --uninstall)
        uninstall=yes
        shift
        ;;
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
      --require-verification)
        require_verification=yes
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

  if [ "$uninstall" = yes ]; then
    if [ "$channel_given" = yes ] || [ -n "$pinned_version" ] \
      || [ "$print_tag" = yes ] || [ "$require_verification" = yes ]; then
      fail "--uninstall removes what is already installed and takes no other options"
    fi
  elif [ -n "$pinned_version" ]; then
    [ "$channel_given" = no ] \
      || fail "--version and --channel are alternatives: a pinned version names one release, a channel tracks whatever is newest"
    case "$pinned_version" in
      [0-9]*) ;;
      *) fail "--version takes a bare version like 0.1.0, not '$pinned_version'" ;;
    esac
  else
    case " $CHANNELS " in
      *" $channel "*) ;;
      *) fail "unknown channel '$channel' (choose one of: $CHANNELS)" ;;
    esac
  fi

  if [ -z "$install_dir" ]; then
    [ -n "${HOME:-}" ] || fail "HOME is unset — set POSTFLIGHT_INSTALL_DIR to choose a destination"
    install_dir="$HOME/.local/bin"
  fi

  # Under sudo, HOME is root's: the install would land where the person who
  # typed it has no reason to look.
  if [ -n "${SUDO_USER:-}" ] && [ -z "${POSTFLIGHT_INSTALL_DIR:-}" ]; then
    fail "running under sudo would install into root's home rather than $SUDO_USER's. Nothing here needs root — rerun without sudo, or set POSTFLIGHT_INSTALL_DIR to install somewhere shared."
  fi

  command -v sed > /dev/null 2>&1 || fail "sed is required but not on PATH"

  # Removal needs no network and no release listing, so it runs before any of
  # the machinery installing does.
  if [ "$uninstall" = yes ]; then
    uninstall_installation
    exit 0
  fi

  for tool in curl grep; do
    command -v "$tool" > /dev/null 2>&1 || fail "$tool is required but not on PATH"
  done

  if command -v cosign > /dev/null 2>&1; then
    verify_signature=yes
  elif [ "$require_verification" = yes ]; then
    fail "--require-verification was given but cosign is not on PATH (https://docs.sigstore.dev/cosign/installation)"
  else
    verify_signature=no
  fi

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
  trap cleanup EXIT INT TERM

  if [ -n "$pinned_version" ]; then
    # Named outright, so no listing is consulted and the answer cannot drift.
    tag="$TAG_PREFIX/v$pinned_version"
  elif resolve_tag "$channel" > "$work/tag"; then
    tag="$(cat "$work/tag")"
  else
    available=""
    for candidate in $CHANNELS; do
      if resolve_tag "$candidate" > /dev/null; then
        available="$available $candidate"
      fi
    done
    [ -n "$available" ] || fail "no postflight CLI release exists on any channel yet"
    suggested="${available# }"
    suggested="${suggested%% *}"
    fail "no release on the '$channel' channel yet. Available channels:$available
Install from one explicitly, for example:

  curl -fsSL https://guardianintelligence.org/postflight/install.sh | sh -s -- --channel $suggested"
  fi

  if [ "$print_tag" = yes ]; then
    printf '%s\n' "$tag"
    exit 0
  fi

  log "installing $tag ($target)"

  # Release-asset URLs percent-encode the slash in the tag; a literal slash
  # resolves only through a redirect that not every proxy follows.
  tag_path="$(printf '%s' "$tag" | sed 's|/|%2F|g')"
  asset="postflight-$target"

  fetch "$DOWNLOAD_URL/$tag_path/$asset" "$work/$asset" \
    || fail "$tag carries no $asset asset — check that the release exists and covers this platform"
  fetch "$DOWNLOAD_URL/$tag_path/checksums.txt" "$work/checksums.txt" \
    || fail "$tag has no checksums.txt asset"

  expected="$(grep "^[0-9a-f]\{64\}  $asset\$" "$work/checksums.txt" | cut -d' ' -f1)"
  [ -n "$expected" ] || fail "checksums.txt carries no entry for $asset"
  actual="$(sha256_of "$work/$asset")"
  [ "$actual" = "$expected" ] || fail "sha256 mismatch for $asset (expected $expected, got $actual)"

  if [ "$verify_signature" = yes ]; then
    fetch "$DOWNLOAD_URL/$tag_path/$asset.sigstore.json" "$work/$asset.sigstore.json" \
      || fail "$tag has no signature bundle for $asset"
    # cosign narrates to stderr on success too; only a failure should speak.
    if ! cosign_output="$(cosign verify-blob \
      --bundle "$work/$asset.sigstore.json" \
      --certificate-identity "$BUILD_IDENTITY" \
      --certificate-oidc-issuer "$ISSUER" \
      "$work/$asset" 2>&1)"; then
      printf '%s\n' "$cosign_output" >&2
      fail "signature verification failed for $asset"
    fi
    log "signature verified against $BUILD_IDENTITY"
  else
    log "cosign is not on PATH — checksum verified, signature not. Install cosign and rerun with --require-verification for the full chain."
  fi

  mkdir -p "$install_dir" || fail "could not create $install_dir"
  chmod 0755 "$work/$asset"

  # The binary takes its final name only once it has proven it runs, and is
  # staged in the destination directory so a noexec TMPDIR cannot fail it.
  staged="$install_dir/.postflight.install.$$"
  mv "$work/$asset" "$staged" || fail "could not write to $install_dir"
  if ! version_out="$("$staged" version 2>&1)"; then
    printf '%s\n' "$version_out" >&2
    fail "the $target binary from $tag does not run on this machine"
  fi
  # Only the first line is the version: a reinstall over an existing one finds
  # the old receipt still in place, and the binary reports its provenance under
  # the version it leads with.
  version="$(printf '%s\n' "$version_out" | head -n 1)"
  version="${version##* }"
  mv "$staged" "$install_dir/postflight" || fail "could not write $install_dir/postflight"
  staged=""

  # A pinned version tracks no channel, and recording one would tell
  # `postflight version` it is following something it is not.
  if [ -n "$pinned_version" ]; then
    receipt_channel=""
  else
    receipt_channel="$channel"
  fi
  write_receipt "$install_dir/postflight" "$receipt_channel" \
    "$tag" "$version" "$target" "$actual"

  log "installed postflight $version to $install_dir/postflight"

  case ":${PATH:-}:" in
    *":$install_dir:"*) ;;
    *) log "$install_dir is not on your PATH — add it with: export PATH=\"$install_dir:\$PATH\"" ;;
  esac
}

main "$@"
