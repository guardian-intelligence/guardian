# Management Infrastructure Evidence Matrix

This matrix tracks the evidence required before the management-cluster
infrastructure work can be called complete.

| Component | Desired state source | Load test report | DR drill report | Single-node outage report | Status |
| - | - | - | - | - | - |
| Talos / Kubernetes API VIP | `src/infrastructure/talm/` | pending | pending | pending | evidence commands declared |
| LINSTOR / DRBD storage | `src/infrastructure/base/storage/`, `src/infrastructure/evidence/storage-smoke.yaml` | pending | pending | pending | opt-in smoke fixture declared |
| OpenBao | `src/infrastructure/base/openbao/` | pending | pending | pending | evidence commands declared |
| CNPG / Postgres | `src/infrastructure/base/apps/postgres.yaml`, `src/infrastructure/base/backups/managed-databases.yaml`, `src/infrastructure/evidence/database-dr.yaml` | pending | pending | pending | opt-in backup/restore fixture declared |
| Harbor | `src/infrastructure/base/apps/harbor.yaml`, `src/products/company/site:push-harbor` | pending | pending | pending | publish target declared; live push/pull pending |
| ClickHouse | `src/infrastructure/base/apps/clickhouse.yaml`, `src/infrastructure/base/backups/managed-databases.yaml`, `src/infrastructure/evidence/database-dr.yaml` | pending | pending | pending | opt-in backup/restore fixture declared |
| Cozystack Dashboard | `src/infrastructure/base/cozystack/platform.yaml`, `src/infrastructure/evidence/http-load.yaml` | pending | pending | pending | opt-in HTTP fixture declared |
| Public ingress / DNS | `src/infrastructure/bootstrap/cloudflare-dns/` | pending | pending | pending | adopted, not applied |
| Dev tenant | `src/infrastructure/base/tenants/environments.yaml` | pending | pending | pending | evidence commands declared |
| Gamma tenant | `src/infrastructure/base/tenants/environments.yaml` | pending | pending | pending | evidence commands declared |
| Company site dev/gamma/prod | `src/products/company/site/`, `src/infrastructure/base/products/company-site.yaml`, `src/environments/{dev,gamma,prod}/environment.yaml`, `src/infrastructure/evidence/http-load.yaml` | pending | pending | pending | image built locally; opt-in HTTP fixture declared; Harbor publish pending |

Evidence must be gathered from live systems after convergence. Render checks,
OpenTofu validation, and state import are prerequisite evidence only; they do
not satisfy load, disaster-recovery, or single-node outage requirements.

Evidence collection commands and pass/fail criteria live in
`docs/runbooks/management-evidence.md`.
