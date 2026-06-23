# Cozystack Management Validation, 2026-06-23

Cluster: `guardian-mgmt`

Validated source revision: `f6322aa6c438749cc405f354a214932a5e6f9ae6`

This report summarizes standard tool output from `aspect infra ...`, `kubectl`,
`k6`, ORAS, pgbench, ClickHouse benchmark, cert-manager, and Cozystack backup
resources. Raw command logs were kept under `/tmp` during the session and are
not durable repo state.

## Source And Live Gates

- `aspect infra validate` passes on this branch without provider credentials.
  The task now isolates validate-only OpenTofu metadata with `TF_DATA_DIR` so a
  developer checkout's cached `.terraform` backend state cannot make source
  validation depend on R2 credentials.
- `aspect infra live --revision f6322aa6c438749cc405f354a214932a5e6f9ae6`
  passed before and after the outage drill. Source-controller reported the
  expected revision reconciled.

## Database Load

`aspect infra load-db` passed for Postgres/CNPG and ClickHouse in
`root`, `dev`, `gamma`, and `prod`.

| Component | Stage | Result | Summary |
| - | - | - | - |
| Postgres | root | Pass | 12,731 transactions, 2.350 ms average latency, 849.108 tps |
| Postgres | dev | Pass | 12,539 transactions, 2.389 ms average latency, 836.432 tps |
| Postgres | gamma | Pass | 12,279 transactions, 2.439 ms average latency, 819.090 tps |
| Postgres | prod | Pass | 14,144 transactions, 2.121 ms average latency, 942.954 tps |
| ClickHouse | root | Pass | 20 queries, 159.557 QPS |
| ClickHouse | dev | Pass | 20 queries, 161.879 QPS |
| ClickHouse | gamma | Pass | 20 queries, 159.303 QPS |
| ClickHouse | prod | Pass | 20 queries, 160.506 QPS |

These are smoke loads, not capacity benchmarks.

## Backup And Restore

OpenBao snapshot smoke passed before and after the outage drill. The final run
reported all three replicas initialized and unsealed, Raft autopilot
`Healthy: true`, failure tolerance `1`, and a pod-local snapshot SHA256 before
removing the snapshot from the pod.

ClickHouse backup-only drills passed in all stages:

| Stage | BackupJob | Phase | Backup Phase |
| - | - | - | - |
| root | `guardian-root-clickhouse-20260623t025213z` | `Succeeded` | `Ready` |
| dev | `guardian-dev-clickhouse-20260623t025242z` | `Succeeded` | `Ready` |
| gamma | `guardian-gamma-clickhouse-20260623t025311z` | `Succeeded` | `Ready` |
| prod | `guardian-prod-clickhouse-20260623t025339z` | `Succeeded` | `Ready` |

ClickHouse full restore is not complete. A dev restore drill created temporary
`ClickHouse/guardian-drill-restore`; the app reached `Ready=True`, but its
generated `ClickHouseInstallation/clickhouse-guardian-drill-restore` remained
`InProgress` with `hostsAdded=1`, no restore target Pods or StatefulSets, and
events stopped after PDB/service creation. The temporary restore target was
deleted and the source app returned to `Ready=True` and `WorkloadsReady=True`.

Postgres backup/restore remains intentionally untested because the
`Postgres/guardian` app specs do not yet declare concrete object-store backup
coordinates. The reusable CNPG `BackupClass` and OpenBao-projected credentials
exist, but the app backup blocks must be wired before a meaningful CNPG DR
drill can pass.

## Harbor

Root Harbor registry data-path smoke passed:

- Host: `harbor.guardianintelligence.org`
- Repository/tag: `library/guardian-smoke:guardian-root-20260623t025441z`
- Payload SHA256: `305cd1b63b03d44fe18918b28e14a74735857ff0550cb3d9a9f490a7bfd40f86`
- OCI digest pushed and pulled:
  `sha256:0e7bd86ef1d2912cab8932823c2362c2893343efe4a632f7fe501ff3ba70246b`

Environment Harbor registry smoke is blocked before ORAS authentication because
`harbor.dev.gi.org`, `harbor.gamma.gi.org`, and `harbor.prod.gi.org` resolve to
`34.195.138.161`, not the management cluster.

## HTTP And DNS

Public DNS is not converged to the OpenTofu topology.

Observed stale records:

- `dev.gi.org`, `gamma.gi.org`, `prod.gi.org`, `harbor.dev.gi.org`,
  `harbor.gamma.gi.org`, and `harbor.prod.gi.org` resolve to `34.195.138.161`.
- `guardianintelligence.org` resolves to excluded Verself prod
  `206.223.228.99`.
- `dashboard.guardianintelligence.org`, `keycloak.guardianintelligence.org`,
  `harbor.guardianintelligence.org`, `s3.guardianintelligence.org`, and
  `api.guardianintelligence.org` still include `67.213.115.113`.

The checked-in OpenTofu topology expects public ingress records to use:

- `206.223.228.101`
- `45.250.254.119`
- `206.223.228.87`

`DELETE_ME.env` was not present in the checkout during this session, and no
provider credential environment variables were loaded, so
`aspect infra dns-apply --mode apply` was not run.

Diagnostic k6 runs with `--host-overrides` showed:

| Surface | Result | Cause |
| - | - | - |
| Dashboard | Pass | Succeeds when `dashboard.guardianintelligence.org` and `keycloak.guardianintelligence.org` are pinned to a current cluster node |
| Company-site dev/gamma/prod | Fail | TLS certificate presented as `ingress.local`, not the requested host |
| Harbor dev/gamma/prod HTTP | Fail | TLS certificate presented as `ingress.local`, not the requested host |

cert-manager state explains the TLS failures:

- `Certificate/company-site-tls` in dev/gamma/prod is
  `Ready=False`, `Reason=DoesNotExist`.
- `Certificate/harbor-guardian-ingress-tls` in dev/gamma/prod is
  `Ready=False`, `Reason=DoesNotExist`.
- ACME HTTP-01 challenges for those hosts are pending because public self-checks
  time out against stale DNS; prod `guardianintelligence.org` additionally saw
  a self-check `404`.
- `dashboard-web-tls`, `web-tls` for Keycloak, root Harbor TLS, and root S3 TLS
  are `Ready=True`.

## Single-Node Outage

`aspect infra node-outage-drill` was run against `ash-water` after changing the
outage contract to Kubernetes-native disruption health.

The standard drain path worked: `ash-water` was cordoned and drained through
`kubectl drain` without `--force` or `--disable-eviction`; PDBs controlled the
eviction boundary. During the outage phase, root/dev/gamma/prod Postgres,
Harbor, and ClickHouse apps reported `Ready=True` and `WorkloadsReady=True`, and
dashboard deployments remained `Available=True`.

OpenBao stayed available under the intended `local-retain` contract:
`OpenBao/guardian` reported `Ready=True`, and
`StatefulSet/openbao-guardian` reported `readyReplicas=2` out of `replicas=3`,
meeting quorum `2`.

Company-site stayed available under its PDB contract while topology spread kept
the third replica intentionally pending:

- dev: `currentHealthy=2`, `desiredHealthy=2`, `disruptionsAllowed=0`
- gamma: `currentHealthy=2`, `desiredHealthy=2`, `disruptionsAllowed=0`
- prod: `currentHealthy=2`, `desiredHealthy=2`, `disruptionsAllowed=0`

Recovery completed unattended: the helper uncordoned `ash-water`, waited for the
returned OpenBao container to exist, detected `openbao-guardian-2` as sealed,
unsealed it with the cluster-local `unseal-key`, waited for
`StatefulSet/openbao-guardian` to return to `3/3`, and then required
dev/gamma/prod company-site deployments to return to `3/3`. The drill completed
with `node outage drill completed: node=ash-water`.

Post-drill `aspect infra live --revision c987e948570407e7d0711ef6bcbb306634a18e14`
passed; source-controller reported the same merged main revision reconciled.

## Remaining Work

1. Run `aspect infra dns-apply --mode apply` with Route53, Cloudflare, and R2
   backend credentials, then verify public DNS contains only the current
   OpenTofu public ingress IPs.
2. Let cert-manager complete HTTP-01 issuance, then rerun public k6 loads for
   company-site, dashboard, Harbor HTTP, and root/dev/gamma/prod Harbor ORAS
   registry smoke without `--host-overrides`.
3. Investigate ClickHouse restore target reconciliation. Backup-only works, but
   full restore did not create restore target pods in dev.
4. Wire concrete Postgres backup coordinates into the `Postgres/guardian` app
   specs, then run CNPG backup and restore drills.
