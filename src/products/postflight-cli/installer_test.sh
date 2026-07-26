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
done

if [[ "$failures" -gt 0 ]]; then
  echo "$failures installer assertion(s) failed" >&2
  exit 1
fi
