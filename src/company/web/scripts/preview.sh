#!/usr/bin/env bash
# Company-site preview: the dev server from this checkout, rendering prod
# letters from Directus with drafts included, at http://127.0.0.1:4252.
# Code edits hot-reload; letter edits in the Studio show up within a minute.
#
# Needs cluster access (kubectl in .guardian/tools/bin). Reads the Directus
# admin credential to see drafts; it only ever lives in this process's env.
set -euo pipefail

web_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$web_dir/../../.." && pwd)"
export PATH="$repo_root/.guardian/tools/bin:$PATH"

namespace=tenant-guardian-prod
directus_port="${COMPANY_PREVIEW_DIRECTUS_PORT:-28055}"

# Port-forwards drop on pod restarts and idle timeouts; keep one alive for
# the life of the preview. Bound to 127.0.0.1 explicitly: kubectl otherwise
# settles for ::1 when another app holds the IPv4 port, and requests to
# 127.0.0.1 then hang on the squatter.
(
  while true; do
    kubectl -n "$namespace" port-forward --address 127.0.0.1 svc/directus "$directus_port:80" >/dev/null 2>&1 || true
    sleep 1
  done
) &
forward_pid=$!
trap 'kill "$forward_pid" 2>/dev/null; pkill -f "svc/directus $directus_port:80" 2>/dev/null || true' EXIT

until curl -sf -m 2 "http://127.0.0.1:$directus_port/server/ping" >/dev/null; do sleep 1; done

DIRECTUS_PASSWORD="$(kubectl -n "$namespace" get secret directus-admin-credential -o jsonpath='{.data.password}' | base64 -d)"
export DIRECTUS_PASSWORD
export DIRECTUS_EMAIL=admin@guardianintelligence.org
export DIRECTUS_URL="http://127.0.0.1:$directus_port"
export DIRECTUS_INCLUDE_DRAFTS=true

echo "company-site preview: http://127.0.0.1:4252 (drafts included)"
cd "$web_dir"
vp dev
