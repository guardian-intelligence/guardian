# guardian

Playground for the Talos-native rewrite of the Verself host and bootstrap
layers. The dev site is the experiment surface: a single bare-metal box that
is repeatedly wiped, reinstalled with Talos, and converged from zero to a
healthy OpenBao by control loops, with disaster recovery restored from an
offsite snapshot pinned by digest.

Scope today: one infrastructure component (OpenBao as an OCI image), the
pinned Talos toolchain, and the `guardian` CLI. Kubernetes manifests, the
GitOps layer, and provider-driven reinstall land as the tracer progresses.

See `docs/architecture/bootstrap.md` for the layer model and the DR contract.

## Layout

```
src/guardian-cli/                  controller-side CLI (Bazel-built Go binary)
src/infrastructure-components/     deployable components, one OCI image each
src/sites/                         per-site Talos schematics and config patches
docs/architecture/                 design documents
```

## Developer setup

Every tool a developer touches is version-pinned in the repo. `bazel` is
pinned in `.bazeliskrc` (version + binary sha256, honored by bazelisk, the
Aspect CLI, and guardian itself); `aspect` is pinned in `.aspect/version.axl`
(the native launcher pin, plus a guardian-sha256 line guardian verifies);
`talosctl` and `kubectl` ride in the guardian binary's runfiles.

```bash
# One-time: install symlinks (aspect, bazel, talosctl, kubectl, guardian)
# into ~/.local/bin. Each name points at the guardian binary, which
# dispatches on argv[0] and resolves the enclosing workspace's pins on every
# invocation.
bazelisk run //src/guardian-cli/cmd/guardian -- tools install
# Equivalent: `aspect dev install`. Remove with `guardian tools uninstall`.

bazel build //...                  # the sha256-verified pinned bazel
guardian run talosctl -- version   # any pinned tool, without symlinks
```

Repo task surface (gazelle, bzlmod, dev symlinks) lives in `.aspect/tasks/`;
list it with `aspect help`.

## Quickstart

Run from the repo root; site paths are repo-root relative.

```bash
bazelisk build //...

# Pinned component versions.
bazelisk run //src/guardian-cli/cmd/guardian -- version

# Build the OpenBao image and load it into the local container runtime.
bazelisk run //src/infrastructure-components/openbao:load
```

## Wipe drill

A full from-zero convergence of the dev site. The only inputs are the
workspace clone and the ability to authenticate to the box; no provider
API, no registry credentials, and no Guardian-hosted infrastructure.

Run drills from the repo root: the configured bootstrap path is stored
absolute, but the paths inside `bootstrap.yaml` (schematic, patches), the
Crossplane environment bundle, and component manifests are repo-root relative.

```bash
# One-time: point guardian at the site's bootstrap facts. The path is stored
# absolute in ${XDG_CONFIG_HOME:-~/.config}/guardian/config.yaml; inspect
# with `guardian config`.
guardian config bootstrap src/sites/dev/bootstrap.yaml

# Down: wipe to Talos maintenance mode; waits until the Talos API answers.
# A configured Talos node is reset over its API; a generic Linux node is
# kexec'd into the factory maintenance image over SSH (caller's ambient ssh
# auth; the node downloads the kernel from the factory itself).
# Up: verify disk inventory, apply machine config (install to disk),
# bootstrap etcd, fetch kubeconfig, stand up the seed registry, push every
# workspace-built image into it by digest, apply components.
guardian down --yes && guardian up
```

Both verbs also accept an explicit `<bootstrap.yaml>` positional argument,
which overrides the configured bootstrap path.

`up` probes runtime truth rather than recorded state: a node answering the
authenticated Talos API gets its regenerated config re-applied; a node in
maintenance mode gets disk-inventory verification, first install, and etcd
bootstrap. Talos cluster secrets persist in
`~/.local/state/guardian/<cluster>/` so cluster identity survives wipe
drills; OpenBao init/restore/unseal remain operator decisions after
convergence.

Workload images travel controller -> node, not through any external
registry: `up` applies the seed-registry Deployment (the one digest-pinned
public image in the chain), pushes each Bazel-built OCI layout through a
kubectl port-forward, and renders manifests referencing
`registry.guardian.internal/<component>@<built digest>` — a virtual name the
Talos machine config maps to the in-cluster registry, so what runs is
byte-for-byte what the workspace built.

## Pins

Every third-party byte is pinned by sha256 in a `*.MODULE.bazel` include or
an `oci.pull` digest. Component versions consumed by the CLI are compile-time
constants in `src/guardian-cli/cmd/guardian/main.go`; changing what the fleet
runs is a reviewed commit, never a flag.

| Component | Version  | Where                                                        |
| --------- | -------- | ------------------------------------------------------------ |
| Bazel     | 9.1.0    | `.bazeliskrc` (version + binary sha256), `.bazelversion`      |
| Aspect CLI | 2026.17.17 | `.aspect/version.axl` (version + guardian-sha256)          |
| Go        | 1.26.4   | `src/guardian-cli/go.mod` (workspace: `bazel.go.work`)        |
| Talos     | v1.13.4  | `src/guardian-cli/tools/talosctl/talosctl.MODULE.bazel`       |
| kubectl   | v1.36.1  | `src/guardian-cli/tools/kubectl/kubectl.MODULE.bazel`         |
| OpenBao   | v2.5.4   | `src/infrastructure-components/openbao/openbao.MODULE.bazel`  |
