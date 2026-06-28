# Tribal knowledge

## Cloudflare bootstrap credentials

Guardian uses three separate Cloudflare credentials for the ASH management
cluster edge. Keep the runtime controller token narrow; keep provisioner and
state credentials outside the cluster so a wiped cluster can still be rebuilt.

| Credential | Consumer | Durable home | Scope |
| - | - | - | - |
| `cloudflare_external_dns_api_token` | ExternalDNS in `external-dns` | OpenBao path `kv/guardian/guardian-mgmt/tenant-guardian/dns/external-dns`, projected by External Secrets Operator | Zone `guardianintelligence.org`: `Zone Read`, `DNS Read`, `DNS Write` |
| `cloudflare_dns_lb_provisioner_api_token` | OpenTofu root `src/infrastructure/bootstrap/guardian-mgmt-dns` during edge bootstrap or recovery | Off-cluster break-glass or CI secret store; injected only into the apply environment | Account: `Load Balancing: Monitors and Pools Read`, `Load Balancing: Monitors and Pools Write`; Zone `guardianintelligence.org`: `Zone Read`, `Load Balancers Read`, `Load Balancers Write` |
| `cloudflare_r2_access_key_id` and `cloudflare_r2_secret_access_key` | OpenTofu S3-compatible backend for repo-owned state | Off-cluster break-glass or CI secret store; injected only into the apply environment | R2 `Object Read & Write`, scoped to the OpenTofu state bucket `guardian-vault` |

User must pay $10/mo to enable CloudFlare LB with 3 endpoints (1 for each ingress node). This is not enabled by default.
