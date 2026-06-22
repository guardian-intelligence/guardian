# 2026-06-21 IaC Bootstrap Status

## Scope

This report covers the first repository-only infrastructure increment for the
Guardian management cluster: OpenTofu roots for Latitude substrate adoption and
Cloudflare DNS adoption, shared management-cluster inventory, and base
Cozystack desired state for root/dev/gamma tenants, storage, networking,
CNPG/Postgres, Harbor, ClickHouse, OpenBao, and the company-site deployment
envelope.

## Evidence Collected

- `aspect infra fmt` completed successfully for the OpenTofu roots.
- `aspect infra validate` completed successfully for both OpenTofu roots with
  remote backends disabled.
- `bazelisk build //:build //src/infrastructure/bootstrap/cloudflare-dns:root //src/infrastructure/bootstrap/guardian-mgmt:root`
  completed successfully.
- `aspect infra render-base` rendered 690 lines of Kubernetes YAML with the
  repo-pinned `kubectl` target.
- `bazelisk build //src/products/company/site:image //src/products/company/site:image.digest`
  completed successfully and produced
  `sha256:708390f2a646b7286fdc29c6d9bc0cc789932aa7ae6fa899ce436084e5435277`.
- `bazelisk run //src/products/company/site:load` loaded the company-site image
  into Podman as `localhost/guardian/company-site:dev`; local HTTP probes for
  `/`, `/healthz`, and `/metrics` passed.
- `jq empty src/infrastructure/inventory/guardian-mgmt.json` completed
  successfully.
- `git diff --check` completed successfully.

## DNS Adoption

Cloudflare DNS state was initialized in the R2-backed OpenTofu backend and four
existing A records were imported into state: apex, `dev`, `gamma`, and `oci`.
No DNS apply was run.

The reviewed plan after import showed:

- 14 records to add;
- 3 records to update;
- 0 records to destroy.

The planned updates include moving the apex and `oci.guardianintelligence.org`
from `206.223.228.99` to the management-node public IP set, and normalizing TTLs
to Cloudflare automatic TTL.

## Live Cluster Probe

No `guardian-mgmt` kubeconfig was present under the new cluster state path.
Available dev/nonprod/gamma kubeconfigs failed Kubernetes API certificate
verification against the management-node public IPs, which is consistent with
stale local state after host rebootstrap. The available prod kubeconfig reached
the excluded single-node Verself prod host, so no further checks were run
against it.

## Not Yet Satisfied

- No infrastructure component load test has run yet.
- No component disaster-recovery drill has run yet.
- No single-node outage exercise has run yet.
- The company-site image is built, smoke-tested locally, and referenced by
  digest, but it has not yet been pushed into the management-cluster Harbor
  registry.
- No live Kubernetes, Latitude, or Cloudflare DNS apply was run from this
  increment.

## Follow-Up Risk

Public ingress currently uses multiple public node A records. That is the right
repo-declared shape for the current Latitude private-VLAN topology, but it is
DNS-level failover, not a health-checked public L2 VIP. Single-node outage
testing must verify the real client-visible behavior before prod traffic moves.
