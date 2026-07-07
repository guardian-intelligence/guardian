#!/usr/bin/env bash
# Checks every Flux OCIRepository CR pinned by tag+digest (Helm/Flux OCI
# charts pulled by source-controller, not containerd) against the upstream
# registry: does the pinned tag still resolve to the pinned digest? Check-only — never edits anything. Purely
# stateless: one anonymous registry-token exchange plus one manifest lookup
# per ref (curl+jq, already on every GitHub Actions runner), no cluster
# access, no cosign/oras/crane binary.
#
#   check-oci-ref-drift.sh <repo-root>
#
# Exit 0: every pinned tag resolves to its pinned digest.
# Exit 1: at least one mismatch — the report names each file and the exact
#         digest line to paste, so the fix is a copy-paste in the same PR
#         (these pins are rendered inputs to the generated union lock, so
#         no separate inventory edit exists).
# Exit 2: a lookup genuinely failed (network/auth), distinct from a mismatch.
#
# This guards one mistake: a human bumping a tag and pasting a wrong or stale
# digest. It deliberately does NOT watch for upstream re-tagging over time —
# Flux pulls by the pinned digest and never re-resolves the tag, so a moved
# tag has no effect on the cluster and is not worth a standing schedule.
set -uo pipefail

root="${1:?usage: check-oci-ref-drift.sh <repo-root>}"
mismatches=0

# Anonymous pull token + manifest digest lookup. ghcr.io-shaped (token host
# == registry host) because every OCIRepository in this repo today points at
# ghcr.io; if a non-ghcr host is ever added here, this needs a per-registry
# token-endpoint table (docker.io's is auth.docker.io, not docker.io itself).
resolve_digest() {
  local host="$1" repo="$2" tag="$3" token
  token="$(curl -fsSL "https://${host}/token?scope=repository:${repo}:pull" | jq -r .token)" || return 1
  curl -fsSL \
    -H "Authorization: Bearer ${token}" \
    -H "Accept: application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.index.v1+json" \
    -D - -o /dev/null \
    "https://${host}/v2/${repo}/manifests/${tag}" \
    | tr -d '\r' | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}'
}

while IFS= read -r -d '' file; do
  # Only OCIRepository CRs pinning both tag and digest qualify —
  # HelmRepository (external-dns.yaml) and the dark-mode OCIRepository
  # (sync-dark, tracks a branch tip, no digest pin) are out of scope.
  grep -q '^kind: OCIRepository$' "$file" || continue
  grep -q '^\s*tag:' "$file" || continue
  grep -q '^\s*digest: sha256:' "$file" || continue

  url="$(grep -m1 '^\s*url: oci://' "$file" | sed -E 's/^\s*url: oci:\/\///')"
  host="${url%%/*}"
  repo="${url#*/}"
  tag="$(grep -m1 '^\s*tag:' "$file" | awk '{print $2}')"
  pinned="$(grep -m1 '^\s*digest: sha256:' "$file" | awk '{print $2}')"

  live="$(resolve_digest "$host" "$repo" "$tag")"
  if [[ -z "$live" ]]; then
    echo "check-oci-ref-drift: ERROR could not resolve ${host}/${repo}:${tag}" >&2
    exit 2
  fi

  if [[ "$live" == "$pinned" ]]; then
    echo "ok: ${repo}:${tag} == ${pinned}" >&2
    continue
  fi

  mismatches=$((mismatches + 1))
  cat >&2 <<EOF
MISMATCH: ${file}
  ${host}/${repo}:${tag}
  pinned:   ${pinned}
  upstream: ${live}
  fix: set 'digest: ${live}' in this file
EOF
done < <(find "$root/src/infrastructure" -name '*.yaml' -print0)

if [[ $mismatches -gt 0 ]]; then
  echo "check-oci-ref-drift: $mismatches pinned ref(s) do not match upstream" >&2
  exit 1
fi
