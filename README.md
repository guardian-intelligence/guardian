# guardian

Guardian is being cut over to a Cozystack-native management cluster. The
post-Kubernetes desired state is declared in this repo and converged by
OpenTofu, Talm, Talos, Flux, Cozystack, and standard Kubernetes controllers.
Local commands are limited to bootstrap, validation, load, and disaster-recovery
drills.

## Layout

```text
.aspect/                       durable Aspect task surface
src/infrastructure/bootstrap/  OpenTofu bootstrap roots
src/infrastructure/base/       root management-cluster Kubernetes desired state
src/infrastructure/cmd/        infra validation, load, and DR helpers
src/infrastructure/load/       k6 scripts used by infra load helpers
src/infrastructure/talm/       Talm chart for the management control plane
src/products/company/          active TanStack company website artifact
src/tools/                     repo-pinned external tool archives
```

## Commands

Run from the repo root.

```bash
aspect infra validate

aspect infra tofu-init

aspect infra bootstrap

aspect infra openbao-drill \
  --mode init-unseal

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
cluster-local OpenBao app, and `aspect infra openbao-apply` applies the standard
OpenBao API state through a live port-forward. `aspect infra
observability-drill` creates a short root Postgres pgbench job, then queries
VictoriaMetrics and VictoriaLogs for that exact workload and the CNPG scrape
path. Postgres and ClickHouse backups use Cozystack 1.5's platform-managed
`BackupClass/cozy-default` and system bucket via
`spec.backup.useSystemBucket: true`; the repo does not carry Guardian-specific
backup strategies or per-app backup credential Secrets.

Available live debugging CLIs are repo-pinned `kubectl`, `talosctl`, `helm`,
`k6`, and ORAS through the focused `aspect infra ...` tasks.

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

Run `aspect tidy` before publishing changes.
