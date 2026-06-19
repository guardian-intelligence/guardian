# guardian

Guardian is being cut over to a Cozystack-native bootstrap.

The active `guardian` CLI has one job: host come-up. It takes a
pre-provisioned bare-metal node that starts from stock Ubuntu, renders a
Cozystack Talm project from repo facts, installs Talos with the pinned
boot-to-talos tool, bootstraps Kubernetes, writes an encrypted genesis secret
bundle, and hands off to the Cozystack installer. Runtime platform configuration
belongs to Flux/Crossplane after that handoff.

The previous source tree is preserved under `src-old/` for reference only. It
is ignored by Bazel and is not part of the active command surface.

## Layout

```text
src/guardian/                  new Go CLI and host-bootstrap packages
src/hosts/                     physical host facts and Talos inputs
src/clusters/                  nonprod/prod cluster bootstrap intent
src/environments/              Crossplane/Flux environment bags
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
  up -f src/hosts/ash-bm-004/host.cue --output json
```

Plan mode is the default. Destructive execution requires `--execute`, a host
assignment that allows destructive bootstrap, and a cluster config that
explicitly opts into stock-Ubuntu-to-Talos install:

```cue
bootstrap: {
  destructive: true
  requireMaintenance: true
  targetState: "stock-ubuntu"
  genesis: ageRecipients: ["age1..."]
}
```

Without `bootstrap.genesis.ageRecipients`, `guardian up --execute` refuses
before running any mutating command. The recipient is public age material; the
private identity stays in the operator's own secret store.

With `--execute`, `guardian up` writes live status to stderr by default.
Interactive terminals get a compact Bubble Tea step tree; redirected runs get
plain status lines. Structured `--output json|yaml|toml` stays on stdout. Use
`--status=plain` to force log lines or `--status=off` to disable the status
channel.

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
rendered node config, kubeconfig, and operation evidence. Nothing from the
genesis set is committed to the repo.

## Pinned Tools

The CLI resolves these from Bazel runfiles, never from `PATH`:

| Tool | Pin |
| - | - |
| Go | `src/guardian/go.mod` |
| Talm | `src/tools/talm/talm.MODULE.bazel` |
| talosctl | `src/tools/talosctl/talosctl.MODULE.bazel` |
| boot-to-talos | `src/tools/boot-to-talos/boot-to-talos.MODULE.bazel` |
| kubectl | `src/tools/kubectl/kubectl.MODULE.bazel` |
| Helm | `MODULE.bazel` |

Run `aspect tidy` before publishing changes.
