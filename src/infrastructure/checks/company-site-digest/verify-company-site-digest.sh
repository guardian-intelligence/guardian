#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: verify-company-site-digest.sh DIGEST_FILE OUT_FILE" >&2
  exit 2
fi

digest_file="$1"
out_file="$2"
digest="$(tr -d '[:space:]' <"${digest_file}")"
image_ref="oci.guardianintelligence.org/guardian/company-site@${digest}"

if [[ ! "${digest}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  echo "invalid company-site digest in ${digest_file}: ${digest}" >&2
  exit 1
fi

require_exact_ref_count() {
  local path="$1"
  local expected="$2"
  local actual
  local total_refs

  actual="$(grep -Fc "${image_ref}" "${path}" || true)"
  total_refs="$(grep -Ec 'oci\.guardianintelligence\.org/guardian/company-site@sha256:[a-f0-9]{64}' "${path}" || true)"
  if [[ "${actual}" -ne "${expected}" || "${total_refs}" -ne "${expected}" ]]; then
    echo "${path}: expected ${expected} company-site reference(s) to ${image_ref}; matched ${actual}; total digest refs ${total_refs}" >&2
    exit 1
  fi
}

require_harbor_evidence_digest() {
  local path="$1"
  local actual

  actual="$(sed -n -E 's/^[[:space:]]*digest="\$\{DIGEST:-(sha256:[a-f0-9]{64})\}".*/\1/p' "${path}" | head -n 1)"
  if [[ "${actual}" != "${digest}" ]]; then
    echo "${path}: expected Harbor evidence default digest ${digest}; got ${actual:-missing}" >&2
    exit 1
  fi
}

require_exact_ref_count "src/infrastructure/base/products/company-site.yaml" 3
require_exact_ref_count "src/environments/dev/environment.yaml" 1
require_exact_ref_count "src/environments/gamma/environment.yaml" 1
require_exact_ref_count "src/environments/prod/environment.yaml" 1
require_harbor_evidence_digest "src/infrastructure/evidence/harbor-oci-read.yaml"

printf 'company-site digest %s verified\n' "${digest}" >"${out_file}"
