#!/usr/bin/env bash
# Render or remove a company-site PR preview in a checkout of the `previews`
# orchestration branch. Called by company-site-preview.yml (render) and
# company-site-preview-teardown.yml (remove); runnable locally the same way.
#
#   company-site-preview.sh render <previews-checkout> <pr-number> <image-digest> <head-sha>
#   company-site-preview.sh remove <previews-checkout> <pr-number>
#
# A preview is one values-only HelmRelease: the manifest shape lives in the
# reviewed chart on main (src/infrastructure/deployments/company/previews/
# chart), so the only machine-written state per PR is (pr, digest, sha).
#
# Branch layout (machine-managed):
#   manifests/kustomization.yaml   regenerated here from the pr-*.yaml files
#   manifests/preview-index.yaml   static seed ConfigMap (never rewritten)
#   manifests/pr-<N>.yaml          one preview HelmRelease
set -euo pipefail

die() { echo "company-site-preview: $*" >&2; exit 1; }

regen_root_kustomization() {
  local root="$1"
  {
    echo "# Machine-managed by .github/scripts/company-site-preview.sh — do not edit."
    echo "apiVersion: kustomize.config.k8s.io/v1beta1"
    echo "kind: Kustomization"
    echo "resources:"
    echo "  - preview-index.yaml"
    for f in "$root"/pr-*.yaml; do
      [ -f "$f" ] || continue
      echo "  - $(basename "$f")"
    done
  } > "$root/kustomization.yaml"
}

render() {
  local tree="$1" pr="$2" digest="$3" sha="$4"
  [[ "$pr" =~ ^[0-9]+$ ]] || die "pr-number must be numeric, got: $pr"
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || die "image-digest must be sha256:<64 hex>, got: $digest"
  [[ "$sha" =~ ^[a-f0-9]{40}$ ]] || die "head-sha must be a full 40-hex sha, got: $sha"

  mkdir -p "$tree/manifests"
  cat > "$tree/manifests/pr-${pr}.yaml" << EOF
# Machine-managed preview of company-site for PR #${pr} (head ${sha}).
# Chart: src/infrastructure/deployments/company/previews/chart on main.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: company-site-pr-${pr}
  namespace: tenant-guardian-previews
  labels:
    app.kubernetes.io/name: company-site-preview
    app.kubernetes.io/part-of: guardian
    guardian.dev/product: company
    guardian.dev/preview: pr-${pr}
spec:
  interval: 10m
  timeout: 5m
  chart:
    spec:
      chart: ./src/infrastructure/deployments/company/previews/chart
      sourceRef:
        kind: GitRepository
        name: guardian
        namespace: cozy-fluxcd
  install:
    remediation:
      retries: 3
  upgrade:
    remediation:
      retries: 3
  values:
    pr: ${pr}
    commitSha: "${sha}"
    image:
      digest: "${digest}"
EOF

  regen_root_kustomization "$tree/manifests"
  echo "rendered manifests/pr-${pr}.yaml (image @${digest})"
}

remove() {
  local tree="$1" pr="$2"
  [[ "$pr" =~ ^[0-9]+$ ]] || die "pr-number must be numeric, got: $pr"
  rm -f "$tree/manifests/pr-${pr}.yaml"
  regen_root_kustomization "$tree/manifests"
  echo "removed manifests/pr-${pr}.yaml"
}

cmd="${1:-}"; shift || true
case "$cmd" in
  render) [ $# -eq 4 ] || die "render needs <previews-checkout> <pr-number> <image-digest> <head-sha>"; render "$@" ;;
  remove) [ $# -eq 2 ] || die "remove needs <previews-checkout> <pr-number>"; remove "$@" ;;
  *) die "usage: company-site-preview.sh render|remove ..." ;;
esac
