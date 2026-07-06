# company-site PR previews

Vercel-style preview deploys for `src/products/viteplus-monorepo/apps/guardianintelligence-web`. Each open PR that
touches the site gets its own deployment at
`https://pr-<N>.guardianintelligence.org`, updated on every push, torn down on
close.

## How it flows

1. `.github/workflows/company-site-preview.yml` runs on the PR: builds the
   image with Bazel, pushes it to ghcr.io **by digest only** (no tag — the
   `edge` tag stays owned by merges to main), and commits a **values-only
   HelmRelease** (`manifests/pr-<N>.yaml`: pr number, image digest, head sha)
   to the `previews` orchestration branch. The manifest shape lives in the
   reviewed chart here on main (`./chart`).
2. Flux (`guardian-company-previews` in base/flux/sync.yaml) watches the
   `previews` branch and applies `manifests/` into `tenant-guardian-previews`,
   pruning whatever the branch no longer declares; helm-controller renders
   each release from the chart (same cross-namespace GitRepository sourceRef
   pattern Cozystack itself uses for tenant apps).
3. `pr-<N>.guardianintelligence.org` resolves with **no per-preview DNS
   record at all**: `*.guardianintelligence.org` is already a Cloudflare
   Load Balancer pointed at the same ASH origin pool the apex uses
   (`src/infrastructure/bootstrap/guardian-mgmt-dns/main.tf`), proxied and
   covered by the zone's Universal SSL wildcard cert, same as prod. Routing
   to the right preview happens entirely via the chart's per-PR `Ingress`
   Host rule inside the shared tenant-root ingress controller — nothing
   needs to wait on DNS-record creation or propagation, which used to be a
   real (and sometimes flaky — Cloudflare occasionally challenges the
   GitHub Actions runner IP mid-poll) contributor to how long a preview
   took to become reachable. A `DNSEndpoint` CR (external-dns, CRD source)
   was used here previously; removed once the always-present wildcard was
   confirmed to already cover the same hostnames.
4. The workflow polls the preview URL until the served
   `guardian:commit-sha` meta equals the PR head SHA, then posts/updates a
   sticky PR comment with the URL.
5. On PR close, `company-site-preview-teardown.yml` deletes
   `manifests/pr-<N>/` from the `previews` branch; Flux prunes the workload.
   Nothing DNS-side to clean up — the wildcard record was never per-preview.

This directory holds the halves that live on `main`: the Cilium policy pair
admitting the tenant-root ingress controller to preview pods (trust-sensitive)
and the `company-site-preview` chart (the reviewed manifest shape). The per-PR
HelmReleases live only on the `previews` branch and are entirely
machine-managed — do not edit that branch by hand.

Trust note: anything pushed to the `previews` branch is applied by Flux into
`tenant-guardian-previews` (constrained by the Kustomization's
`targetNamespace`). Branch write access == preview-namespace deploy access;
the workflows only run for same-repo PRs, never forks.
