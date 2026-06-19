# guardian

Guardian is being cut over to a Cozystack-native bootstrap.

The active `guardian` CLI has one job: host come-up. It takes an existing
pre-provisioned bare-metal node, optionally reboots that same node through
Latitude's iPXE reinstall flow into Talos maintenance, renders and applies a
Cozystack Talm project, bootstraps Kubernetes, installs the Cozystack
operator/platform package, writes an encrypted genesis secret bundle, and
applies a default hello-world handoff manifest.

The previous source tree is preserved under `src-old/` for reference only. It
is ignored by Bazel and is not part of the active command surface.

## Bootstrap Timing

Guardian's four-minute target is measured from **provisioning complete to etcd
quorum**. Provider-side allocation, disk erase, iPXE reinstall, power cycling,
and rescue/OOB recovery are outside that clock. Full Cozystack platform
convergence and the hello-world handoff are also outside that clock; they remain
required `guardian up` handoff checks after quorum.

For single-node dev, quorum means the one control-plane member has bootstrapped
etcd and backs the Kubernetes API. For multi-node clusters, quorum means a
majority of the intended control-plane members are participating in etcd.

## Layout

```text
src/guardian/                  new Go CLI and host-bootstrap packages
src/clusters/guardian-dev/     first Cozystack-native dev cluster config
src/schemas/                   CUE schemas for first-party config
src/tools/                     pinned runfile tool archives
src-old/                       archived pre-Cozystack implementation
docs/architecture/             design notes
```

## Commands

Run from the repo root.

```bash
bazel test //...

bazel run //src/guardian/cmd/guardian -- \
  up src/clusters/guardian-dev/up.cue --output json
```

Plan mode is the default. Destructive execution requires `--execute`, at least
one genesis age recipient, and a CUE config that explicitly opts into
maintenance-mode reimage:

```cue
bootstrap: {
  destructive: true
  requireMaintenance: true
  targetState: "talos-maintenance"
}
```

Supply recipients either in `bootstrap.genesis.ageRecipients`, with repeated
`--genesis-age-recipient age1...` flags, or with
`GUARDIAN_GENESIS_AGE_RECIPIENTS`. The recipient is public age material; the
private identity stays in the operator's own secret store.

With `--execute`, `guardian up` writes simple live status to stderr by default:
Heroku-style status lines with short descriptions. Structured
`--output json|yaml|toml` stays on stdout. Use `--status=tui` for the
experimental compact in-place view, or `--status=off` to disable the status
channel.

The dev config also includes an existing Latitude server id. `guardian up` can
call Latitude only for that existing server's GET and reinstall endpoints; it
does not contain a server-create path.

## Secret Bootstrap

Cozystack-managed OpenBao comes after the platform exists, so it cannot own the
cluster genesis secrets. `guardian up` handles that gap by keeping the Talm
project under local operator state:

```text
${XDG_STATE_HOME:-~/.local/state}/guardian/clusters/<cluster>/
```

After `talm kubeconfig`, it writes:

```text
genesis.bundle.tar.age
```

The encrypted bundle contains a manifest plus `talm.key`, `secrets.yaml`, the
rendered node config, kubeconfig, operation evidence, and generated handoff
manifests. Nothing from the genesis set is committed to the repo.

## Pinned Tools

The CLI resolves these from Bazel runfiles, never from `PATH`:

| Tool | Pin |
| - | - |
| Go | `src/guardian/go.mod` |
| Talm | `src/tools/talm/talm.MODULE.bazel` |
| talosctl | `src/tools/talosctl/talosctl.MODULE.bazel` |
| kubectl | `src/tools/kubectl/kubectl.MODULE.bazel` |
| Helm | `MODULE.bazel` |

Run `aspect tidy` before publishing changes.
