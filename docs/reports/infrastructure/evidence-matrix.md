# Management Infrastructure Evidence Matrix

This matrix tracks the evidence required before the management-cluster
infrastructure work can be called complete.

| Component | Desired state source | Load test report | DR drill report | Single-node outage report | Status |
| - | - | - | - | - | - |
| Talos / Kubernetes API VIP | `src/infrastructure/talm/` | pending | pending | pending | desired state only |
| LINSTOR / DRBD storage | `src/infrastructure/base/storage/` | pending | pending | pending | desired state only |
| OpenBao | `src/infrastructure/base/openbao/` | pending | pending | pending | desired state only |
| CNPG / Postgres | `src/infrastructure/base/apps/postgres.yaml` | pending | pending | pending | desired state only |
| Harbor | `src/infrastructure/base/apps/harbor.yaml` | pending | pending | pending | desired state only |
| ClickHouse | `src/infrastructure/base/apps/clickhouse.yaml` | pending | pending | pending | desired state only |
| Cozystack Dashboard | `src/infrastructure/base/cozystack/platform.yaml` | pending | pending | pending | desired state only |
| Public ingress / DNS | `src/infrastructure/bootstrap/cloudflare-dns/` | pending | pending | pending | adopted, not applied |
| Dev tenant | `src/infrastructure/base/tenants/environments.yaml` | pending | pending | pending | desired state only |
| Gamma tenant | `src/infrastructure/base/tenants/environments.yaml` | pending | pending | pending | desired state only |
| Company site dev/gamma/prod | `src/products/company/site/`, `src/infrastructure/base/products/company-site.yaml`, `src/environments/{dev,gamma,prod}/environment.yaml` | pending | pending | pending | desired state only |

Evidence must be gathered from live systems after convergence. Render checks,
OpenTofu validation, and state import are prerequisite evidence only; they do
not satisfy load, disaster-recovery, or single-node outage requirements.
