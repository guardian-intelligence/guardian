# guardian

Guardian runs on a Cozystack-native management cluster. The desired state is
declared in this repo and converged by OpenTofu, Talm, Talos, Flux, Cozystack,
and standard Kubernetes controllers.
Local commands are limited to bootstrap, validation, load, and disaster-recovery
drills.

## Commands

Run from the repo root.

```bash
aspect infra validate

aspect infra tofu-init

aspect infra bootstrap

aspect infra bundle

aspect infra converged

aspect infra openbao-drill

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
OpenBao uses static auto-unseal and OpenBao self-init; see
`src/infrastructure/runbooks/openbao-static-seal-self-init.md`.
`aspect infra bundle` builds the offline bundle into a fresh `dist/bundle/`:
it gates on the Tier-1 lock tests, projects
`src/infrastructure/bootstrap/bundle/images.lock` into a Hauler manifest,
syncs every locked artifact into a content store, saves the portable
`haul.tar.zst`, and records a bundle manifest with the git revision and
digests. The haul plus the source-built Hauler binary, the pinned flux CLI,
the repo checkout, and the operator custody bundle are the complete inputs to
a dark-uplink cold boot (`src/infrastructure/runbooks/cold-boot-bootstrap.md`).
`aspect infra converged` verifies every declared Flux Kustomization is Ready
at the expected revision; workload and component health gate readiness via
Flux health checks declared in the manifests (`healthChecks`/`healthCheckExprs`).
`aspect infra openbao-drill` verifies OpenBao status (initialized, unsealed,
HA-enabled, one raft cluster ID across members). `aspect infra
observability-drill` creates a short root Postgres pgbench job, then queries
VictoriaMetrics and VictoriaLogs for that exact workload and the CNPG scrape
path. Guardian runs no in-cluster object storage; database backups will target
off-cluster Cloudflare R2 (path pending), and the repo does not carry
Guardian-specific backup strategies or per-app backup credential Secrets.

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
| OpenBao CLI (`bao`) | `src/tools/openbao/openbao.MODULE.bazel` |
| Hauler | `src/tools/hauler/go.mod` (built from source; `//src/tools/hauler`) |
| Flux CLI | `src/tools/flux/flux.MODULE.bazel` |
| curl | `src/tools/curl/curl.MODULE.bazel` |
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
