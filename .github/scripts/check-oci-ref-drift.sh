#!/usr/bin/env bash
# Finds every Flux OCIRepository CR in the repo pinned by tag+digest (the
# "artifact section" of images.lock: Helm/Flux OCI charts pulled by
# source-controller, not containerd) and checks whether the pinned tag still
# resolves to the pinned digest upstream. Purely stateless: one anonymous
# registry-token exchange plus one manifest HEAD per ref, no cluster access,
# no cosign/oras/crane binary — just curl+jq, already on every GitHub Actions
# runner. This is the generalization of the manual "artifact section" scrape
# images.lock's header still describes.
#
#   check-oci-ref-drift.sh <repo-root>
#
# On drift, rewrites both the OCIRepository YAML's `digest:` field and the
# matching images.lock line in place, and prints the list of changed files to
# stdout (one path per line). Prints nothing and exits 0 if nothing drifted.
# Exits non-zero only on a genuine lookup failure (network/auth error) —
# a clean "tag still matches" is not a failure.
set -euo pipefail

root="${1:?usage: check-oci-ref-drift.sh <repo-root>}"
lock="$root/src/infrastructure/bootstrap/bundle/images.lock"
changed_files=()

# Anonymous pull token + manifest digest lookup. ghcr.io-shaped (token host
# == registry host) because every OCIRepository in this repo today points at
# ghcr.io; if a non-ghcr host is ever added here, this needs a per-registry
# token-endpoint table (docker.io's is auth.docker.io, not docker.io itself).
resolve_digest() {
  local host="$1" repo="$2" tag="$3" token
  token="$(curl -fsSL "https://${host}/token?scope=repository:${repo}:pull" | jq -r .token)"
  curl -fsSL \
    -H "Authorization: Bearer ${token}" \
    -H "Accept: application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.index.v1+json" \
    -D - -o /dev/null \
    "https://${host}/v2/${repo}/manifests/${tag}" \
    | tr -d '\r' | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}'
}

while IFS= read -r -d '' file; do
  # Only files with a Flux OCIRepository CR whose ref pins both tag and
  # digest qualify — HelmRepository (external-dns.yaml) and the dark-mode
  # OCIRepository (sync-dark, tracks a branch tip, not a release tag) are
  # deliberately out of scope.
  grep -q '^kind: OCIRepository$' "$file" || continue
  grep -q '^\s*tag:' "$file" || continue
  grep -q '^\s*digest: sha256:' "$file" || continue

  url="$(grep -m1 '^\s*url: oci://' "$file" | sed -E 's/^\s*url: oci:\/\///')"
  host="${url%%/*}"
  repo="${url#*/}"
  tag="$(grep -m1 '^\s*tag:' "$file" | awk '{print $2}')"
  pinned_digest="$(grep -m1 '^\s*digest: sha256:' "$file" | awk '{print $2}')"

  live_digest="$(resolve_digest "$host" "$repo" "$tag")"
  if [[ -z "$live_digest" ]]; then
    echo "check-oci-ref-drift: could not resolve $host/$repo:$tag (empty digest)" >&2
    exit 1
  fi

  if [[ "$live_digest" == "$pinned_digest" ]]; then
    echo "check-oci-ref-drift: $repo:$tag unchanged ($pinned_digest)" >&2
    continue
  fi

  echo "check-oci-ref-drift: DRIFT $repo:$tag $pinned_digest -> $live_digest" >&2
  sed -i "s|digest: ${pinned_digest}|digest: ${live_digest}|" "$file"
  changed_files+=("$file")

  # images.lock lines for artifact-section entries are bare `ref@sha256:...`
  # (no oci:// scheme, host+repo joined with '/'); update the matching line
  # by its (repo) prefix so the tag suffix embedded in some refs (e.g.
  # ":1.10.8@sha256:...") is preserved.
  if grep -q "^${host}/${repo}:${tag}@sha256:${pinned_digest#sha256:}\$" "$lock"; then
    sed -i "s|^${host}/${repo}:${tag}@sha256:${pinned_digest#sha256:}\$|${host}/${repo}:${tag}@sha256:${live_digest#sha256:}|" "$lock"
    changed_files+=("$lock")
  else
    echo "check-oci-ref-drift: WARNING no matching images.lock line found for ${host}/${repo}:${tag}@${pinned_digest} — updated $file but could not update images.lock, needs a manual line added" >&2
  fi
done < <(find "$root/src/infrastructure" -name '*.yaml' -print0)

if [[ ${#changed_files[@]} -eq 0 ]]; then
  exit 0
fi

printf '%s\n' "${changed_files[@]}" | sort -u
