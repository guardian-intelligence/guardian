#!/usr/bin/env bash
# Exercises dist/install.sh without a network: every POSIX shell on the box
# parses it, and its channel resolution is driven against synthetic release
# listings through POSTFLIGHT_RELEASES_DIR.
set -euo pipefail

installer="$(realpath "$1")"
mixed="$(dirname "$(realpath "$2")")"
nightly_only="$(dirname "$(realpath "$3")")"
stable_only="$(dirname "$(realpath "$4")")"

tmp="$(mktemp -d "${TEST_TMPDIR:-/tmp}/installer-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

shells=(sh)
for candidate in dash ash bash; do
  if command -v "$candidate" > /dev/null 2>&1; then
    shells+=("$candidate")
  fi
done

[[ "$(head -n 1 "$installer")" == "#!/bin/sh" ]] \
  || fail "installer must declare #!/bin/sh, got $(head -n 1 "$installer")"

for shell in "${shells[@]}"; do
  "$shell" -n "$installer" || fail "$shell -n rejected the installer"
done

run() { # <shell> <releases dir> [args...]
  local shell="$1" dir="$2"
  shift 2
  env POSTFLIGHT_RELEASES_DIR="$dir" POSTFLIGHT_INSTALL_DIR="$tmp/bin" \
    "$shell" "$installer" "$@" > "$tmp/out" 2> "$tmp/err"
}

expect_tag() { # <shell> <releases dir> <channel> <want>
  local shell="$1" dir="$2" channel="$3" want="$4"
  if ! run "$shell" "$dir" --channel "$channel" --print-tag; then
    fail "$shell: --channel $channel exited nonzero: $(cat "$tmp/err")"
    return
  fi
  local got
  got="$(cat "$tmp/out")"
  [[ "$got" == "$want" ]] \
    || fail "$shell: channel $channel resolved to '$got', want '$want'"
}

expect_missing() { # <shell> <releases dir> <channel> <want available> <want suggestion>
  local shell="$1" dir="$2" channel="$3" available="$4" suggestion="$5"
  if run "$shell" "$dir" --channel "$channel" --print-tag; then
    fail "$shell: channel $channel resolved to '$(cat "$tmp/out")', want a failure"
    return
  fi
  grep -q "Available channels: $available\$" "$tmp/err" \
    || fail "$shell: channel $channel did not report 'Available channels: $available': $(cat "$tmp/err")"
  grep -q -- "--channel $suggestion\$" "$tmp/err" \
    || fail "$shell: channel $channel did not suggest '--channel $suggestion': $(cat "$tmp/err")"
}

# Compact single-line listings, as the API actually serves them.
write_nightly_page() { # <file> <count> <first day>
  local file="$1" count="$2" day="$3" i sep=""
  {
    printf '['
    for ((i = 0; i < count; i++)); do
      printf '%s{"tag_name":"postflight-cli/nightly-%08d","draft":false,"prerelease":true,"body":"nightly"}' \
        "$sep" "$((day + i))"
      sep=","
    done
    printf ']\n'
  } > "$file"
}

write_stable_page() { # <file> <version>
  printf '[{"tag_name":"postflight-cli/v%s","draft":false,"prerelease":false,"body":"stable"}]\n' \
    "$2" > "$1"
}

# A stable release that has fallen off page 1 behind a hundred nightlies.
paged="$tmp/paged"
mkdir -p "$paged"
write_nightly_page "$paged/page-1.json" 100 20260101
write_stable_page "$paged/page-2.json" "2.0.0"

# Deeper than the page budget: the walk has to stop rather than keep spending
# requests, so the stable release on page 11 stays invisible.
deep="$tmp/deep"
mkdir -p "$deep"
for page in {1..10}; do
  write_nightly_page "$deep/page-$page.json" 100 "$((20260101 + page * 100))"
done
write_stable_page "$deep/page-11.json" "3.0.0"

expect_rejected() { # <shell> <description> <args...>
  local shell="$1" what="$2"
  shift 2
  if run "$shell" "$mixed" "$@"; then
    fail "$shell: $what was accepted, want a failure"
  fi
}

# The hazard `curl | sh` is actually criticised for: the shell executes what it
# has parsed, so a connection that dies mid-stream runs half a script. Every
# truncation of this one must reach EOF inside main's body and die there,
# having executed nothing — no install directory, no output.
assert_truncation_inert() { # <shell>
  local shell="$1" size offset home
  size="$(wc -c < "$installer")"
  for ((offset = 1; offset < size; offset += 64)); do
    head -c "$offset" "$installer" > "$tmp/truncated.sh"
    home="$tmp/truncation-home"
    rm -rf "$home"
    mkdir -p "$home"
    env -u POSTFLIGHT_INSTALL_DIR -u SUDO_USER \
      POSTFLIGHT_RELEASES_DIR="$mixed" HOME="$home" \
      "$shell" "$tmp/truncated.sh" --channel nightly \
      > "$tmp/out" 2> "$tmp/err" || true
    # A prefix ending on a definition boundary exits cleanly; what none of them
    # may do is act. Reaching the "installing" line means the download and the
    # write to the install directory were next.
    if [[ -e "$home/.local/bin" ]] || [[ -s "$tmp/out" ]] || grep -q installing "$tmp/err"; then
      fail "$shell: the installer truncated to $offset bytes took effect"
      return
    fi
  done
}

run_uninstall() { # <shell> <home>
  local shell="$1" home="$2"
  env -u POSTFLIGHT_INSTALL_DIR -u SUDO_USER HOME="$home" \
    "$shell" "$installer" --uninstall > "$tmp/out" 2> "$tmp/err"
}

# The receipt is one line of JSON with more keys after binary_path, which is
# exactly what a greedy capture swallows whole — leaving removal pointed at a
# path that does not exist, and reporting success for having removed nothing.
write_receipt() { # <home> <binary>
  mkdir -p "$1/.config/postflight"
  printf '{"schema":1,"method":"install.sh","binary_path":"%s","channel":"nightly","tag":"postflight-cli/nightly-20260726","version":"0.2.0-nightly"}\n' \
    "$2" > "$1/.config/postflight/install-receipt.json"
}

assert_uninstall_paths() { # <shell>
  local shell="$1" home="$tmp/uninstall-home"

  # A binary that will not run is the case this path exists for: the receipt
  # still says where it is, and it has to go.
  rm -rf "$home"
  mkdir -p "$home/.local/bin"
  printf '#!/bin/sh\nexit 3\n' > "$home/.local/bin/postflight"
  chmod +x "$home/.local/bin/postflight"
  write_receipt "$home" "$home/.local/bin/postflight"
  echo '{"access_token":"at"}' > "$home/.config/postflight/credentials.json"
  if run_uninstall "$shell" "$home"; then
    [[ ! -e "$home/.local/bin/postflight" ]] \
      || fail "$shell: --uninstall left the binary behind"
    [[ ! -e "$home/.config/postflight/install-receipt.json" ]] \
      || fail "$shell: --uninstall left the receipt behind"
    [[ ! -e "$home/.config/postflight/credentials.json" ]] \
      || fail "$shell: --uninstall left credentials behind"
  else
    fail "$shell: --uninstall of a broken binary failed: $(cat "$tmp/err")"
  fi

  # A binary that runs removes itself, session included — something a shell
  # script cannot do — so the installer must delegate and then keep its hands off.
  rm -rf "$home"
  mkdir -p "$home/.local/bin"
  printf '#!/bin/sh\nprintf "%%s\\n" "$*" > "%s/invoked"\nexit 0\n' "$home" \
    > "$home/.local/bin/postflight"
  chmod +x "$home/.local/bin/postflight"
  write_receipt "$home" "$home/.local/bin/postflight"
  if run_uninstall "$shell" "$home"; then
    [[ "$(cat "$home/invoked" 2> /dev/null)" == "self uninstall --yes" ]] \
      || fail "$shell: --uninstall did not delegate to the binary, got '$(cat "$home/invoked" 2> /dev/null)'"
    [[ -e "$home/.config/postflight/install-receipt.json" ]] \
      || fail "$shell: --uninstall removed the receipt the binary was told to handle"
  else
    fail "$shell: --uninstall did not delegate: $(cat "$tmp/err")"
  fi

  # Nothing installed: say so rather than exit clean on having done nothing.
  rm -rf "$home"
  mkdir -p "$home"
  if run_uninstall "$shell" "$home"; then
    fail "$shell: --uninstall succeeded with nothing installed"
  fi

  rm -rf "$home"
  mkdir -p "$home"
  if env -u POSTFLIGHT_INSTALL_DIR HOME="$home" "$shell" "$installer" \
    --uninstall --channel nightly > "$tmp/out" 2> "$tmp/err"; then
    fail "$shell: --uninstall alongside --channel was accepted"
  fi
}

for shell in "${shells[@]}"; do
  expect_tag "$shell" "$mixed" nightly "postflight-cli/nightly-20260724"
  expect_tag "$shell" "$mixed" rc "postflight-cli/v0.3.0-rc.1"
  # v9.9.9 is smuggled into the newest release's notes, and the data drop that
  # follows it is the release whose "prerelease": false would be borrowed.
  expect_tag "$shell" "$mixed" stable "postflight-cli/v0.2.0"

  expect_missing "$shell" "$nightly_only" stable "nightly" "nightly"
  expect_missing "$shell" "$nightly_only" rc "nightly" "nightly"
  expect_missing "$shell" "$stable_only" nightly "stable" "stable"

  expect_tag "$shell" "$paged" stable "postflight-cli/v2.0.0"
  expect_tag "$shell" "$paged" nightly "postflight-cli/nightly-20260101"

  expect_missing "$shell" "$deep" stable "nightly" "nightly"

  # A pinned version names its release outright: no listing is consulted, so
  # the answer cannot drift with what has been published since.
  if run "$shell" "$nightly_only" --version 1.2.3 --print-tag; then
    [[ "$(cat "$tmp/out")" == "postflight-cli/v1.2.3" ]] \
      || fail "$shell: --version 1.2.3 resolved to '$(cat "$tmp/out")'"
  else
    fail "$shell: --version 1.2.3 exited nonzero: $(cat "$tmp/err")"
  fi
  expect_rejected "$shell" "--version alongside --channel" \
    --version 1.2.3 --channel nightly --print-tag
  expect_rejected "$shell" "--version carrying its own v prefix" \
    --version v1.2.3 --print-tag

  # sudo would install into root's home rather than the caller's, where they
  # would never find it.
  if env -u POSTFLIGHT_INSTALL_DIR POSTFLIGHT_RELEASES_DIR="$mixed" \
    SUDO_USER=someone HOME="$tmp/sudo-home" \
    "$shell" "$installer" --channel nightly --print-tag > "$tmp/out" 2> "$tmp/err"; then
    fail "$shell: an install under sudo was accepted"
  fi
  # ...unless a destination was named, which makes a shared install deliberate.
  if ! env POSTFLIGHT_RELEASES_DIR="$mixed" POSTFLIGHT_INSTALL_DIR="$tmp/bin" \
    SUDO_USER=someone HOME="$tmp/sudo-home" \
    "$shell" "$installer" --channel nightly --print-tag > "$tmp/out" 2> "$tmp/err"; then
    fail "$shell: sudo with an explicit install dir was refused: $(cat "$tmp/err")"
  fi

  assert_uninstall_paths "$shell"
  assert_truncation_inert "$shell"
done

if [[ "$failures" -gt 0 ]]; then
  echo "$failures installer assertion(s) failed" >&2
  exit 1
fi
