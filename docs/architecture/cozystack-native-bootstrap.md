# Cozystack-Native Bootstrap

Status: implementation direction for the new `src/` tree.

Guardian now treats Cozystack as the platform substrate. The `guardian` CLI is
only a controller-side host come-up tool:

```text
existing pre-provisioned bare-metal node
  -> guarded provider reinstall into Talos maintenance, when configured
  -> Talm-rendered Talos config
  -> Kubernetes bootstrap
  -> etcd quorum
  -> Cozystack operator
  -> cozystack.cozystack-platform Package
  -> hello-world handoff marker
```

Everything from the previous bespoke bootstrap tree is preserved under
`src-old/` for reference. It is not part of the new command surface.

## Timing Gate

The four-minute bootstrap target is **provisioning complete to etcd quorum**.
It is not measured from the provider API request that allocates, wipes,
reinstalls, or powers a bare-metal server, and it is not measured through the
full Cozystack platform package or hello-world handoff.

For a provider-backed flow, the clock starts when the node is already
provisioned for Guardian to manage: the server exists, has the expected IP and
hardware identity, and has reached the boot path where Guardian can drive Talos
installation or maintenance. Provider-side allocation, disk erase, iPXE
handoff, and rescue/OOB operations are separate provider timing.

The clock stops when the Kubernetes control plane has established etcd quorum:

- single-node dev: the one control-plane member has bootstrapped etcd and the
  Kubernetes API is backed by that member.
- multi-node gamma/prod: a majority of the intended control-plane members are
  participating in etcd and the API can survive the loss budget implied by that
  topology.

Cozystack operator install, platform package convergence, CNI/node Ready, and
default hello-world apply are still required handoff checks for `guardian up`,
but they are post-quorum platform convergence, not the four-minute quorum gate.
The live Latitude drill that took 679s measured provider iPXE reinstall through
hello-world handoff, so it is useful operational evidence but not the clarified
four-minute quorum measurement.

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
refused unless the merged genesis age recipient set contains at least one age
recipient. Recipients can come from committed CUE, repeated
`--genesis-age-recipient age1...` flags, or
`GUARDIAN_GENESIS_AGE_RECIPIENTS`; the private age identity stays outside the
repo. After `talm kubeconfig`, `guardian up` writes:

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
guardian up <cluster.cue> [--execute] [--genesis-age-recipient age1...] [--output text|json|yaml|toml] [--status auto|tui|plain|off]
```

Without `--execute`, the command prints the planned stages and commands. With
`--execute`, mutation is still refused unless the CUE config declares:

```cue
bootstrap: {
  destructive: true
  requireMaintenance: true
  targetState: "talos-maintenance"
}
```

During execution, the human status channel is stderr. `--status=auto` uses a
Bubble Tea single-pane status view when stderr is interactive and Heroku-style
status lines otherwise. The final result remains on stdout when
`--output json|yaml|toml` is requested, so structured output stays
automation-safe.

The v0 provider adapter is intentionally narrow: Latitude GET server plus
existing-server reinstall to `operating_system=ipxe`, using a Talos Image
Factory schematic from the repo. The adapter refuses mismatched IPs, locked
servers, and prod-looking names, and it has no server-create path. Rescue-mode
recovery and OOB serial control remain later adapters.

## Pinned Tools

The command resolves tools from Bazel runfiles:

- `talm`
- `talosctl`
- `kubectl`
- `helm`

It never relies on `PATH`, Homebrew, curl install scripts, or host-installed
tooling for correctness.
