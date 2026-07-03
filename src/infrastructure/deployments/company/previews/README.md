# company-site PR previews

Vercel-style preview deploys for `src/products/company/web`. Each open PR that
touches the site gets its own deployment at
`https://pr-<N>.guardianintelligence.org`, updated on every push, torn down on
close.

## How it flows

1. `.github/workflows/company-site-preview.yml` runs on the PR: builds the
   image with Bazel, pushes it to ghcr.io **by digest only** (no tag — the
   `edge` tag stays owned by merges to main), renders per-PR manifests, and
   commits them to the `previews` orchestration branch under
   `manifests/pr-<N>/`.
2. Flux (`guardian-company-previews` in base/flux/sync.yaml) watches the
   `previews` branch and applies `manifests/` into `tenant-guardian-previews`,
   pruning whatever the branch no longer declares.
3. Each preview ships a `DNSEndpoint` CR; external-dns (CRD source) creates a
   Cloudflare-proxied CNAME `pr-<N>` → apex, so TLS terminates at the
   Cloudflare edge exactly like prod (single-label hostname keeps it inside
   the Universal SSL wildcard).
4. The workflow polls the preview URL until the served
   `guardian:commit-sha` meta equals the PR head SHA, then posts/updates a
   sticky PR comment with the URL.
5. On PR close, `company-site-preview-teardown.yml` deletes
   `manifests/pr-<N>/` from the `previews` branch; Flux prunes the workload
   and external-dns (policy: sync) removes the DNS record.

This directory holds the static, trust-sensitive half that lives on `main`:
the Cilium policy pair admitting the tenant-root ingress controller to
preview pods. The per-PR halves live only on the `previews` branch and are
entirely machine-managed — do not edit that branch by hand.

Trust note: anything pushed to the `previews` branch is applied by Flux into
`tenant-guardian-previews` (constrained by the Kustomization's
`targetNamespace`). Branch write access == preview-namespace deploy access;
the workflows only run for same-repo PRs, never forks.
