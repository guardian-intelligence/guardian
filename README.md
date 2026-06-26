# guardian

Guardian is being cut over to a Cozystack-native management cluster. The
post-Kubernetes desired state is declared in this repo and converged by
OpenTofu, Talm, Talos, Flux, Cozystack, and standard Kubernetes controllers.
Local commands are limited to bootstrap, validation, load, and disaster-recovery
drills.

## Commands

Run from the repo root.

```bash
aspect infra validate

aspect infra tofu-init

aspect infra bootstrap

aspect infra openbao-drill \
  --mode init-unseal

AWS_ACCESS_KEY_ID="${cloudflare_r2_access_key_id}" \
AWS_SECRET_ACCESS_KEY="${cloudflare_r2_secret_access_key}" \
aspect infra openbao-apply

aspect infra observability-drill
```

`aspect infra bootstrap` initializes the standard OpenTofu S3 backend from the
checked-in Cloudflare account id in `src/infrastructure/bootstrap/backend.tfvars`
or an explicit `AWS_ENDPOINT_URL_S3` override, prints the standard OpenTofu
management topology outputs, validates the checked-in substrate, refreshes the
gitignored Talm kubeconfig, runs the Talos L2 gate, upgrades the Cozystack
installer/operator to the repo-pinned version.
`aspect infra upgrade-cozystack` is the narrow day-two path for existing
clusters when only the Cozystack installer/operator release needs to move.
`aspect infra openbao-drill --mode init-unseal` initializes/unseals the
tenant-scoped Guardian KMS OpenBao authority in `tenant-guardian-kms`, and
`aspect infra openbao-apply` applies the standard OpenBao API state through a
live port-forward. `openbao-apply` requires the off-cluster R2 state credentials
as `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` before it reads any OpenBao
bootstrap material. In apply mode, `openbao-apply` then runs
`openbao-drill --mode api-state` to verify KV v2, transit keys, policies, and
Kubernetes auth roles for ExternalDNS and third-party integrations. Use an
explicit `--namespace tenant-root` only for the legacy root break-glass OpenBao
instance during migration. `aspect infra observability-drill`
creates a short root Postgres pgbench job, then queries VictoriaMetrics and
VictoriaLogs for that exact workload and the CNPG scrape path. Postgres and
ClickHouse backups use Cozystack 1.5's platform-managed `BackupClass/cozy-default`
and system bucket via
`spec.backup.useSystemBucket: true`; the repo does not carry Guardian-specific
backup strategies or per-app backup credential Secrets.

The live management tasks set `GODEBUG=tlsmlkem=0` for repo-pinned Go clients
that talk to guardian-mgmt Kubernetes or Talos endpoints. The current public
control-plane path can time out during larger default Go TLS ClientHello
handshakes; this compatibility setting keeps operator checks repeatable until
the underlying network/TLS path is fixed.

Available live debugging CLIs are repo-pinned and can be installed as local
shims:

```bash
aspect tools install
eval "$(aspect tools path)"
aspect tools uninstall
```

The default shim directory is `.guardian/tools/bin`. Pass
`--bin-dir "${HOME}/.local/bin"` to install into an existing user bin directory.
`aspect tools uninstall` removes only the known shim names from that directory.

Generated Talm secrets, rendered node configs, kubeconfigs, and local operator
state stay out of Git.

## Pinned Tools

| Tool | Pin |
| - | - |
| Go | `MODULE.bazel` / `go.mod` |
| Aspect CLI | `.aspect/version.axl` |
| OpenTofu | `MODULE.bazel` |
| Talm | `src/tools/talm/talm.MODULE.bazel` |
| talosctl | `src/tools/talosctl/talosctl.MODULE.bazel` |
| kubectl | `src/tools/kubectl/kubectl.MODULE.bazel` |
| k6 | `src/tools/k6/k6.MODULE.bazel` |
| ORAS | `src/tools/oras/oras.MODULE.bazel` |
| Cilium CLI | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| Hubble CLI | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| Stern | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| Velero CLI | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| kubectl-cnpg | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| doggo DNS client | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| step TLS/certificate CLI | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| ClickHouse CLI | `src/tools/debug-clis/debug-clis.MODULE.bazel` |
| psql / pgbench | `src/tools/debug-clis/debug-clis.MODULE.bazel` |

Run `aspect tidy` before publishing changes.
