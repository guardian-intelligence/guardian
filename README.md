# guardian

Guardian is being cut over to a Cozystack-native management cluster.

The active `guardian` CLI surface is deliberately narrow: it manages
host/bootstrap come-up paths and delegates post-Kubernetes desired state to the
repo's Aspect, OpenTofu, Talm, Talos, Flux, and Cozystack configuration. It is
not a generic cluster administration CLI.

The previous source tree is preserved under `src-old/` for reference only. It
is ignored by Bazel and is not part of the active command surface.

## Layout

```text
.aspect/                       durable Aspect task surface
src/guardian/                  Go CLI entrypoints for host/bootstrap come-up
src/infrastructure/bootstrap/  OpenTofu bootstrap roots
src/infrastructure/base/       base management-cluster Kubernetes desired state
src/infrastructure/talm/       Talm chart for the management control plane
src/infrastructure/environments/  dev/gamma/prod tenant desired state
src/products/company/          active TanStack company website artifact
src/tools/                     repo-pinned external tool archives
src-old/                       archived pre-Cozystack implementation
docs/runbooks/                 operator runbooks
```

## Commands

Run from the repo root.

```bash
aspect infra validate

aspect infra bootstrap \
  --revision "<merged-main-commit-sha>"

bazelisk run //src/guardian/cmd/guardian -- \
  up management \
  --revision "<merged-main-commit-sha>"
```

`aspect infra bootstrap` prints the standard OpenTofu management topology
outputs, validates the checked-in substrate, refreshes the gitignored Talm
kubeconfig, runs the Talos L2 gate, and verifies live Flux/source-controller
convergence on the requested merged `main` revision.

Generated Talm secrets, rendered node configs, kubeconfigs, and local operator
state stay out of Git. See
`docs/runbooks/cozystack-mgmt-bringup.md` for the full management-cluster path.

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
