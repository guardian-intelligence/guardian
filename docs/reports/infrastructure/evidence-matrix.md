# Management Infrastructure Evidence Matrix

This matrix tracks the evidence required before the management-cluster
infrastructure work can be called complete.

| Component | Desired state source | Load test report | DR drill report | Single-node outage report | Status |
| - | - | - | - | - | - |
| Talos / Kubernetes API VIP | `src/infrastructure/talm/`, `src/infrastructure/evidence/capture-management-evidence.sh` | `2026-06-22-talos-kubernetes-api-vip.md` | same report | same report | API VIP load capture declared; live execution pending |
| LINSTOR / DRBD storage | `src/infrastructure/base/storage/`, `src/infrastructure/evidence/storage-smoke.yaml` | `2026-06-22-linstor-drbd-storage.md` | same report | same report | opt-in smoke fixture declared; live execution pending |
| OpenBao | `src/infrastructure/base/openbao/`, `src/infrastructure/evidence/openbao-load.yaml` | `2026-06-22-openbao.md` | same report | same report | opt-in read/write fixture declared; live execution pending |
| CNPG / Postgres | `src/infrastructure/base/apps/postgres.yaml`, `src/infrastructure/base/backups/managed-databases.yaml`, `src/infrastructure/evidence/database-load.yaml`, `src/infrastructure/evidence/database-dr.yaml` | `2026-06-22-cnpg-postgres.md` | same report | same report | opt-in load and backup/restore fixtures declared; live execution pending |
| Harbor | `src/infrastructure/base/apps/harbor.yaml`, `src/infrastructure/evidence/harbor-oci-read.yaml`, `src/products/company/site:push-harbor` | `2026-06-22-harbor.md` | same report | same report | publish target and digest-read fixture declared; live push/read pending |
| ClickHouse | `src/infrastructure/base/apps/clickhouse.yaml`, `src/infrastructure/base/backups/managed-databases.yaml`, `src/infrastructure/evidence/database-load.yaml`, `src/infrastructure/evidence/database-dr.yaml` | `2026-06-22-clickhouse.md` | same report | same report | opt-in load and backup/restore fixtures declared; live execution pending |
| Cozystack Dashboard | `src/infrastructure/base/cozystack/platform.yaml`, `src/infrastructure/evidence/http-load.yaml` | `2026-06-22-cozystack-dashboard.md` | same report | same report | opt-in HTTP fixture declared; live execution pending |
| Public ingress / DNS | `src/infrastructure/bootstrap/cloudflare-dns/` | `2026-06-22-public-ingress-dns.md` | same report | same report | adopted, not applied |
| Dev tenant | `src/infrastructure/base/tenants/environments.yaml` | `2026-06-22-dev-tenant.md` | same report | same report | report scaffolded; live execution pending |
| Gamma tenant | `src/infrastructure/base/tenants/environments.yaml` | `2026-06-22-gamma-tenant.md` | same report | same report | report scaffolded; live execution pending |
| Prod / root tenant | `src/infrastructure/base/tenants/root.yaml`, `src/environments/prod/environment.yaml` | `2026-06-22-prod-root-tenant.md` | same report | same report | report scaffolded; live execution pending |
| Company site dev/gamma/prod | `src/products/company/site/`, `src/infrastructure/base/products/company-site.yaml`, `src/environments/{dev,gamma,prod}/environment.yaml`, `src/infrastructure/evidence/http-load.yaml` | `2026-06-22-company-site.md` | same report | same report | image built locally; opt-in HTTP fixture declared; Harbor publish pending |

Evidence must be gathered from live systems after convergence. Render checks,
OpenTofu validation, and state import are prerequisite evidence only; they do
not satisfy load, disaster-recovery, or single-node outage requirements.

Evidence collection commands and pass/fail criteria live in
`docs/runbooks/management-evidence.md`. The final checked-in evidence package
should be produced by `aspect infra management-evidence-run` and must include a
passing `aspect infra evidence-verify-suite` report tying the load/DR capture to
the all-node hardware outage capture.
