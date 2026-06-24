# Tribal knowledge

`host.yaml` pins physical facts about the box (NIC MAC, NVMe serials, addressing) — when you reprovision or swap hardware, re-derive them from `talosctl get links/disks --insecure` or the Latitude API before running `up`; never identify a NIC by name, because names are kernel-policy trivia and a dangling one boots the node network-dark (2026-06-10).

## Cloudflare bootstrap credentials

Guardian uses three separate Cloudflare credentials for the ASH management
cluster edge. Keep the runtime controller token narrow; keep provisioner and
state credentials outside the cluster so a wiped cluster can still be rebuilt.

| Credential | Consumer | Durable home | Scope |
| - | - | - | - |
| `cloudflare_external_dns_api_token` | ExternalDNS in `external-dns` | OpenBao path `kv/guardian/guardian-mgmt/tenant-root/dns/external-dns`, projected by External Secrets Operator | Zone `guardianintelligence.org`: `Zone Read`, `DNS Read`, `DNS Write` |
| `cloudflare_dns_lb_provisioner_api_token` | OpenTofu root `src/infrastructure/bootstrap/guardian-mgmt-dns` during edge bootstrap or recovery | Off-cluster break-glass or CI secret store; injected only into the apply environment | Account: `Load Balancing: Monitors and Pools Read`, `Load Balancing: Monitors and Pools Write`; Zone `guardianintelligence.org`: `Zone Read`, `Load Balancers Read`, `Load Balancers Write` |
| `cloudflare_r2_access_key_id` and `cloudflare_r2_secret_access_key` | OpenTofu S3-compatible backend for repo-owned state | Off-cluster break-glass or CI secret store; injected only into the apply environment | R2 `Object Read & Write`, scoped to the OpenTofu state bucket `guardian-vault` |

The bootstrap apply environment also needs the non-secret
`cloudflare_account_id` and the R2 endpoint coordinate
`cloudflare_r2_s3_api_endpoint`. The current OpenTofu backend uses the
S3-compatible R2 access key id and secret access key; a generic
`cloudflare_r2_api_token` is not part of this path.

The Cloudflare account must have Load Balancing enabled before the DNS/LB
OpenTofu root can create monitors, pools, or load balancers. The ASH monitor
uses a 60-second interval, which matches the Pro-plan minimum documented by
Cloudflare; Business and Enterprise can go lower, but the repo default should
stay boring unless the account plan changes.

The Cloudflare account must also have R2 enabled before generating the S3
credentials used by the OpenTofu backend. R2 credentials are S3-compatible
access key material, not the same shape as regular Cloudflare API tokens.
