#!/usr/bin/env bash
set -euo pipefail

installer="$(realpath "$1")"
tmp="$(mktemp -d "${TEST_TMPDIR:-/tmp}/pipe-to-remote-box-installer-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

shells=(sh)
for candidate in dash ash bash; do
  if command -v "$candidate" >/dev/null 2>&1; then
    shells+=("$candidate")
  fi
done

[[ "$(head -n 1 "$installer")" == "#!/bin/sh" ]] \
  || fail "installer must declare #!/bin/sh"
for shell in "${shells[@]}"; do
  "$shell" -n "$installer" || fail "$shell -n rejected the installer"
done

mixed="$tmp/releases-mixed"
stable_only="$tmp/releases-stable-only"
malformed_only="$tmp/releases-malformed-only"
mkdir -p "$mixed" "$stable_only" "$malformed_only"

# `v9.9.9-rc.1` has a false prerelease flag but is not a stable tag. The body
# also contains tag-shaped text that an unanchored parser could mistake for a
# release. The exact tag grammar must skip both before selecting the real tags.
printf '%s\n' '[{"tag_name":"unrelated/v20.0.0","prerelease":false,"body":"tag_name: pipe-to-remote-box/v99.0.0"},{"tag_name":"pipe-to-remote-box/v9.9.9-rc.1","prerelease":false,"body":"candidate"},{"tag_name":"pipe-to-remote-box/nightly-20260731","prerelease":true,"body":"nightly"},{"tag_name":"pipe-to-remote-box/rc-20260730-2359","prerelease":true,"body":"rc"},{"tag_name":"pipe-to-remote-box/v1.2.3","prerelease":false,"body":"stable"}]' \
  >"$mixed/page-1.json"
printf '%s\n' '[{"tag_name":"pipe-to-remote-box/v1.2.3","prerelease":false,"body":"stable"}]' \
  >"$stable_only/page-1.json"
printf '%s\n' '[{"tag_name":"pipe-to-remote-box/nightly-latest","prerelease":true},{"tag_name":"pipe-to-remote-box/rc-20260730?asset=x","prerelease":true},{"tag_name":"pipe-to-remote-box/v1.2.3#fragment","prerelease":false},{"tag_name":"pipe-to-remote-box/v1.2.3 bad","prerelease":false}]' \
  >"$malformed_only/page-1.json"

run_tag() { # <shell> <release-dir> <args...>
  local shell="$1" releases="$2"
  shift 2
  PIPE_TO_REMOTE_BOX_RELEASES_DIR="$releases" \
    PIPE_TO_REMOTE_BOX_INSTALL_DIR="$tmp/tag-bin" \
    "$shell" "$installer" "$@" >"$tmp/out" 2>"$tmp/err"
}

expect_tag() { # <shell> <channel> <expected>
  local shell="$1" channel="$2" expected="$3"
  if ! run_tag "$shell" "$mixed" --channel "$channel" --print-tag; then
    fail "$shell: could not resolve $channel: $(cat "$tmp/err")"
  elif [[ "$(cat "$tmp/out")" != "$expected" ]]; then
    fail "$shell: $channel resolved to '$(cat "$tmp/out")', want '$expected'"
  fi
}

for shell in "${shells[@]}"; do
  expect_tag "$shell" stable "pipe-to-remote-box/v1.2.3"
  expect_tag "$shell" rc "pipe-to-remote-box/rc-20260730-2359"
  expect_tag "$shell" nightly "pipe-to-remote-box/nightly-20260731"

  if ! run_tag "$shell" "$mixed" --version 1.2.3 --print-tag; then
    fail "$shell: an exact version did not resolve: $(cat "$tmp/err")"
  elif [[ "$(cat "$tmp/out")" != "pipe-to-remote-box/v1.2.3" ]]; then
    fail "$shell: exact version resolved to '$(cat "$tmp/out")'"
  fi

  for bad in v1.2.3 1.2 1.2.3-rc.1 '1.2.3?asset=x'; do
    if run_tag "$shell" "$mixed" --version "$bad" --print-tag; then
      fail "$shell: invalid version '$bad' was accepted"
    fi
  done
  if run_tag "$shell" "$mixed" --version 1.2.3 --channel stable --print-tag; then
    fail "$shell: --version and --channel were accepted together"
  fi
  if run_tag "$shell" "$malformed_only" --channel stable --print-tag; then
    fail "$shell: malformed stable tag was selected"
  fi
  if run_tag "$shell" "$malformed_only" --channel rc --print-tag; then
    fail "$shell: malformed rc tag was selected"
  fi
  if run_tag "$shell" "$malformed_only" --channel nightly --print-tag; then
    fail "$shell: malformed nightly tag was selected"
  fi
  if run_tag "$shell" "$stable_only" --channel rc --print-tag; then
    fail "$shell: rc resolved from a stable-only release listing"
  fi
  if run_tag "$shell" "$stable_only" --channel nightly --print-tag; then
    fail "$shell: nightly resolved from a stable-only release listing"
  fi

  if env -u PIPE_TO_REMOTE_BOX_INSTALL_DIR \
    PIPE_TO_REMOTE_BOX_RELEASES_DIR="$mixed" SUDO_USER=somebody HOME="$tmp/root-home" \
    "$shell" "$installer" --version 1.2.3 >"$tmp/out" 2>"$tmp/err"; then
    fail "$shell: implicit sudo install was accepted"
  fi
done

# Signature verification is mandatory, rather than opportunistic when cosign
# happens to be installed. Use an isolated PATH that contains every tool needed
# before the signature gate and deliberately no cosign.
no_cosign="$tmp/no-cosign-tools"
mkdir -p "$no_cosign"
for tool in curl grep sed tr mktemp rm; do
  tool_path="$(command -v "$tool")"
  ln -s "$tool_path" "$no_cosign/$tool"
done
shell_path="$(command -v sh)"
if env PATH="$no_cosign" PIPE_TO_REMOTE_BOX_INSTALL_DIR="$tmp/no-cosign-install" \
  "$shell_path" "$installer" --version 1.2.3 >"$tmp/out" 2>"$tmp/err"; then
  fail "installation succeeded with no Sigstore verifier on PATH"
elif ! grep -q "cosign is required" "$tmp/err"; then
  fail "missing cosign failed for the wrong reason: $(cat "$tmp/err")"
fi
[[ ! -e "$tmp/no-cosign-install/pipe-to-remote-box" ]] \
  || fail "missing cosign still installed a binary"

# A proper prefix of a piped script must never install. Sweep the body coarsely
# and the executable tail byte-by-byte; the latter catches a clipped final call
# that is still syntactically complete.
assert_truncation_inert() { # <shell>
  local shell="$1" size offset tail_start home
  size="$(wc -c <"$installer")"
  tail_start=$((size > 512 ? size - 512 : 1))
  for ((offset = 1; offset + 1 < size; offset += (offset < tail_start ? 64 : 1))); do
    head -c "$offset" "$installer" >"$tmp/truncated.sh"
    home="$tmp/truncated-home"
    rm -rf "$home"
    mkdir -p "$home"
    env -u PIPE_TO_REMOTE_BOX_INSTALL_DIR -u SUDO_USER \
      PIPE_TO_REMOTE_BOX_RELEASES_DIR="$mixed" HOME="$home" \
      "$shell" "$tmp/truncated.sh" --channel stable \
      >"$tmp/out" 2>"$tmp/err" || true
    if [[ -e "$home/.local/bin" ]] || [[ -s "$tmp/out" ]] \
      || grep -q "installing" "$tmp/err"; then
      fail "$shell: installer prefix of $offset bytes took effect"
      return
    fi
  done
}

for shell in "${shells[@]}"; do
  assert_truncation_inert "$shell"
done

case "$(uname -s):$(uname -m)" in
  Linux:x86_64 | Linux:amd64) target="x86_64-unknown-linux-musl" ;;
  Linux:aarch64 | Linux:arm64) target="aarch64-unknown-linux-musl" ;;
  Darwin:x86_64) target="x86_64-apple-darwin" ;;
  Darwin:arm64 | Darwin:aarch64) target="aarch64-apple-darwin" ;;
  *) target="" ;;
esac

if [[ -n "$target" ]]; then
  assets="$tmp/assets"
  tools="$tmp/tools"
  install_dir="$tmp/install"
  asset_name="pipe-to-remote-box-$target"
  mkdir -p "$assets" "$tools" "$install_dir"

  printf '#!/bin/sh\nif [ "$1" = --version ]; then printf "pipe-to-remote-box 1.2.3\\n"; exit 0; fi\nexit 2\n' \
    >"$assets/$asset_name"
  chmod +x "$assets/$asset_name"
  printf '{}\n' >"$assets/$asset_name.sigstore.json"
  if command -v sha256sum >/dev/null 2>&1; then
    digest="$(sha256sum "$assets/$asset_name" | cut -d' ' -f1)"
  else
    digest="$(shasum -a 256 "$assets/$asset_name" | cut -d' ' -f1)"
  fi
  printf '%s  %s\n' "$digest" "$asset_name" >"$assets/checksums.txt"

  # Curl is replaced, not bypassed: the installer still asks for the exact
  # asset names and the stub fails on anything outside the fixture directory.
  cat >"$tools/curl" <<'EOF'
#!/bin/sh
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
[ -n "$out" ] && [ -n "$url" ] || exit 2
name="${url##*/}"
case "$name" in
  pipe-to-remote-box-* | checksums.txt) ;;
  *) exit 3 ;;
esac
cp "$PIPE_TO_REMOTE_BOX_TEST_ASSETS/$name" "$out"
EOF
  cat >"$tools/cosign" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >"$PIPE_TO_REMOTE_BOX_TEST_COSIGN_LOG"
[ "${PIPE_TO_REMOTE_BOX_TEST_COSIGN_FAIL:-0}" = 0 ]
EOF
  chmod +x "$tools/curl" "$tools/cosign"

  run_install() {
    env PATH="$tools:$PATH" \
      PIPE_TO_REMOTE_BOX_RELEASES_DIR="$mixed" \
      PIPE_TO_REMOTE_BOX_INSTALL_DIR="$install_dir" \
      PIPE_TO_REMOTE_BOX_TEST_ASSETS="$assets" \
      PIPE_TO_REMOTE_BOX_TEST_COSIGN_LOG="$tmp/cosign.log" \
      PIPE_TO_REMOTE_BOX_TEST_COSIGN_FAIL="${PIPE_TO_REMOTE_BOX_TEST_COSIGN_FAIL:-0}" \
      "$installer" --version 1.2.3 >"$tmp/out" 2>"$tmp/err"
  }

  if ! run_install; then
    fail "verified install failed: $(cat "$tmp/err")"
  else
    cmp -s "$assets/$asset_name" "$install_dir/pipe-to-remote-box" \
      || fail "install did not preserve the verified asset bytes"
    grep -q -- "--bundle .*\.sigstore\.json" "$tmp/cosign.log" \
      || fail "install did not pass the Sigstore bundle to cosign"
    grep -q -- "--certificate-identity https://github.com/guardian-intelligence/guardian/.github/workflows/pipe-to-remote-box-image.yml@refs/heads/main" "$tmp/cosign.log" \
      || fail "install did not pin the binary build identity"
    grep -q -- "--certificate-oidc-issuer https://token.actions.githubusercontent.com" "$tmp/cosign.log" \
      || fail "install did not pin the GitHub Actions OIDC issuer"
  fi

  # A checksum failure is before signature verification and before replacement.
  printf 'existing installation\n' >"$install_dir/pipe-to-remote-box"
  printf '%064d  %s\n' 0 "$asset_name" >"$assets/checksums.txt"
  rm -f "$tmp/cosign.log"
  if run_install; then
    fail "install accepted a checksum mismatch"
  fi
  [[ "$(cat "$install_dir/pipe-to-remote-box")" == "existing installation" ]] \
    || fail "checksum failure replaced the existing installation"
  [[ ! -e "$tmp/cosign.log" ]] \
    || fail "cosign ran before the checksum gate"

  # Restore the checksum, then prove a failed signature also preserves the old
  # binary even though the staged download itself is executable.
  printf '%s  %s\n' "$digest" "$asset_name" >"$assets/checksums.txt"
  if PIPE_TO_REMOTE_BOX_TEST_COSIGN_FAIL=1 run_install; then
    fail "install accepted a failed Sigstore verification"
  fi
  [[ "$(cat "$install_dir/pipe-to-remote-box")" == "existing installation" ]] \
    || fail "signature failure replaced the existing installation"
fi

if [[ "$failures" -gt 0 ]]; then
  echo "$failures installer assertion(s) failed" >&2
  exit 1
fi
