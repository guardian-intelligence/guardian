#!/usr/bin/env bash
set -euo pipefail

eval "$(scripts/bootstrap.sh path)"

bazelisk build //src/tools/restic:restic >/dev/null
restic_rel="$(bazelisk cquery --output=files //src/tools/restic:restic | tail -n 1)"
restic_bin="$(bazelisk info execution_root)/${restic_rel}"

ram_device=""
mount_point="/Volumes/GuardianCustody"

cleanup() {
  if [[ -n "${ram_device}" ]]; then
    hdiutil detach "${mount_point}" -quiet 2>/dev/null || hdiutil detach "${ram_device}" -quiet 2>/dev/null || true
  fi
}
trap cleanup EXIT

ram_device="$(hdiutil attach -nomount ram://65536 | awk 'NR == 1 { print $1 }')"
diskutil erasevolume APFS GuardianCustody "${ram_device}" >/dev/null
chmod 700 "${mount_point}"

"${restic_bin}" \
  --repo "${HOME}/.guardian/custody/repo" \
  --no-cache \
  restore latest \
  --target "${mount_point}"

env_file="${mount_point}/dev/shm/guardian-custody/custody.env"
if [[ ! -f "${env_file}" ]]; then
  echo "Custody bundle does not contain the expected environment file." >&2
  exit 1
fi

bash -c '
  set -euo pipefail
  set -a
  source "$1"
  set +a
  printf "%s" "$platform_admin_password"
' bash "${env_file}" | pbcopy

echo "Platform Keycloak password copied to clipboard. Username: platform-admin"
