# Tenancy

Guardian uses Cozystack tenants as the regional account boundary. The durable
Guardian tenant identifier is a slug such as `gi-company-prod`; it belongs in
labels, annotations, audit events, release evidence, and secret paths. It is not
automatically the Cozystack `Tenant` object name.

Cozystack 1.5 tenant object names must start with a lowercase letter and use
only lowercase letters and digits. Cozystack derives the namespace from the
parent namespace and child tenant name, so a Tenant named `company` in
`tenant-root` becomes `tenant-company`, and a Tenant named `prod` inside
`tenant-company` becomes `tenant-company-prod`.

The ASH management cluster is laid out under
`src/infrastructure/clusters/ash/`:

- `bootstrap/opentofu/` contains out-of-band regional bootstrap roots.
- `bootstrap/talm/` contains Talos machine configuration inputs.
- `root/` reconciles the `tenant-root` substrate.
- `deployments/` reconciles workloads that run inside child tenants.

The legacy Flux entrypoints under `src/infrastructure/base/` and
`src/infrastructure/deployments/company/prod/` were removed after Flux
reconciled the canonical ASH paths from
`src/infrastructure/clusters/ash/root/flux/sync.yaml`.

The root slice declares existing root-level compatibility tenants for `dev`,
`gamma`, and `prod`. New Guardian-owned infrastructure should live below the
`guardian` tenant chain instead. The first concrete child is `tenant-guardian-kms`,
which hosts the tenant-scoped OpenBao authority while the original
`tenant-root/openbao-guardian` instance remains available for bootstrap and
break-glass continuity during migration.

The standard `aspect infra openbao-drill`, `aspect infra openbao-apply`, node
outage drill, and OpenBao load-test defaults target `tenant-guardian-kms`. The
legacy root instance requires an explicit namespace override or the
`bootstrap-root` OpenBao load-test stage.

The KMS component tenant has explicit stage child tenants:
`tenant-guardian-kms-dev`, `tenant-guardian-kms-gamma`, and
`tenant-guardian-kms-prod`. The live OpenBao runtime still runs in the parent
`tenant-guardian-kms` namespace as a compatibility placement until a later PR
migrates state into `tenant-guardian-kms-prod` with a snapshot/restore drill and
updated OpenBao apply/load defaults.

Milestone order:

1. Keep `tenant-root` limited to Cozystack substrate and bootstrap recovery.
2. Move OpenBao into the Guardian tenancy pattern first as the secrets/transit
   authority.
3. Move other Guardian platform components into the Guardian tenant boundary.
4. Add dev, gamma, and prod subtenants below each Guardian component tenant
   once the parent boundary is proven.

Until a workload has migrated to a real nested Cozystack tenant, label it with
`guardian.dev/tenant-id` so Flux, release evidence, and later migration checks
can track the intended Guardian account without changing runtime placement in
the same PR.
