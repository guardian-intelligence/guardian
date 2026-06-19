# Cozystack-Native Bootstrap

Status: implementation direction for the new `src/` tree.

Guardian now treats Cozystack as the platform substrate. The `guardian` CLI is
only a controller-side host come-up tool:

```text
pre-provisioned Talos maintenance node
  -> Talm-rendered Talos config
  -> Kubernetes bootstrap
  -> Cozystack operator
  -> cozystack.cozystack-platform Package
  -> hello-world handoff marker
```

Everything from the previous bespoke bootstrap tree is preserved under
`src-old/` for reference. It is not part of the new command surface.

## Secret-Zero

Cozystack-managed OpenBAO is an application that exists after Cozystack is up.
It cannot be the source of truth for the secrets required to create the cluster
that hosts it.

The genesis secret set is:

- `talm.key`
- `secrets.yaml`
- `talosconfig`
- `kubeconfig`
- rendered Talm node configs
- operation evidence tying those files to the CUE config digest

These files live only in:

```text
${XDG_STATE_HOME:-~/.local/state}/guardian/clusters/<cluster>/
```

State directories are `0700`; generated secret-bearing files are `0600`.
Nothing from the genesis set is written to the repo. Destructive execution is
refused unless `bootstrap.genesis.ageRecipients` contains at least one age
recipient. After `talm kubeconfig`, `guardian up` writes:

```text
${XDG_STATE_HOME:-~/.local/state}/guardian/clusters/<cluster>/genesis.bundle.tar.age
```

The encrypted tar contains `manifest.json`, `talm.key`, `secrets.yaml`, the
rendered node config, `kubeconfig`, operation evidence, and generated handoff
manifests. Later restore support should move this bundle through offsite storage,
but the bundle is still external state, not source code.

After Cozystack converges, managed OpenBAO can become the runtime and release
secret authority. It does not own Talos/Talm genesis.

## Command

```sh
guardian up -f <cluster.cue> [--execute] [--output text|json|yaml|toml] [--status auto|tui|plain|off]
```

Without `--execute`, the command prints the planned stages and commands. During
execution, the human status channel is stderr. `--status=auto` uses a compact
Bubble Tea step tree when stderr is interactive and plain status lines
otherwise. Structured `--output json|yaml|toml` stays on stdout.

With `--execute`, mutation is still refused unless the CUE config declares:

```cue
bootstrap: {
  destructive: true
  requireMaintenance: true
  targetState: "talos-maintenance"
  genesis: ageRecipients: ["age1..."]
}
```

The v0 live precondition is a pre-provisioned node already booted into Talos
maintenance mode. Provider allocation, iPXE flips, rescue-mode recovery, and OOB
serial control are later adapters.

## Pinned Tools

The command resolves tools from Bazel runfiles:

- `talm`
- `talosctl`
- `kubectl`
- `helm`

It never relies on `PATH`, Homebrew, curl install scripts, or host-installed
tooling for correctness.
