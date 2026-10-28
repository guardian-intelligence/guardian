#!/usr/bin/env bash
# Company-site preview: the dev server from this checkout, rendering prod
# letters from the letters store with drafts included, at
# http://127.0.0.1:4252. Code edits hot-reload; letter edits (made at
# guardianintelligence.org/letters/<slug>/edit) show up within a minute.
#
# Needs cluster access that can read Secret/postgres-products-credentials
# (kubectl in .guardian/tools/bin); the password only lives in this
# process's env.
set -euo pipefail

web_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$web_dir/../../.." && pwd)"
export PATH="$repo_root/.guardian/tools/bin:$PATH"

namespace=tenant-guardian-prod
db_port="${COMPANY_PREVIEW_DB_PORT:-25432}"

# Port-forwards drop on pod restarts and idle timeouts; keep one alive for
# the life of the preview. Bound to 127.0.0.1 explicitly: kubectl otherwise
# settles for ::1 when another app holds the IPv4 port, and requests to
# 127.0.0.1 then hang on the squatter.
(
  while true; do
    kubectl -n "$namespace" port-forward --address 127.0.0.1 svc/postgres-products-rw "$db_port:5432" >/dev/null 2>&1 || true
    sleep 1
  done
) &
forward_pid=$!
trap 'kill "$forward_pid" 2>/dev/null; pkill -f "svc/postgres-products-rw $db_port:5432" 2>/dev/null || true' EXIT

until nc -z 127.0.0.1 "$db_port" 2>/dev/null; do sleep 1; done

LETTERS_DATABASE_PASSWORD="$(kubectl -n "$namespace" get secret postgres-products-credentials -o jsonpath='{.data.letters}' | base64 -d)"
export LETTERS_DATABASE_PASSWORD
export LETTERS_DATABASE_HOST=127.0.0.1
export LETTERS_DATABASE_PORT="$db_port"
export LETTERS_DATABASE_SSL=require
export LETTERS_INCLUDE_DRAFTS=true

echo "company-site preview: http://127.0.0.1:4252 (drafts included)"
cd "$web_dir"
vp dev
