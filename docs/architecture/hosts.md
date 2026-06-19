# Physical host layer

Guardian separates physical capacity from runtime intent.

`src/hosts/<asset-id>/host.cue` is the durable record for one bare-metal
asset. The asset ID is stable across wipes, hostnames, cluster assignments, and
environment changes. A hostname describes the current assignment; it can change
without changing the asset ID.

See `docs/architecture/repo-structure.md` for the full Cozystack-native source
layout and the boundary between OpenTofu, host facts, clusters, environments,
and `guardian up`.

`guardian` owns only host come-up. It reads `host.cue`, generates Talos
machine config from pinned repo inputs, verifies runtime hardware truth, applies
Talos config, bootstraps Kubernetes, seeds the minimum substrate, and hands off
to reconcilers. It does not provision provider servers, choose product versions,
promote releases, or run the application deployment loop.

The checked-in state is split by responsibility:

```
src/hosts/                 physical facts and Talos inputs
src/environments/          post-Kubernetes environment desired state
src/crossplane/packages/   reusable platform and product APIs
src/k8s/bootstrap/         bootstrap-only Kubernetes substrate
```

The host command surface is intentionally small:

```bash
guardian host list
guardian host inspect [host.cue]
guardian host use <host.cue>
guardian down --yes [host.cue]
guardian up [--restore <file|url> --sha256 <hex>] [host.cue]
```

`guardian host list` reads checked-in inventory. `guardian host inspect`
validates a host and prints the effective assignment, including its environment
bundle. `guardian host use` stores the default host path in
`${XDG_CONFIG_HOME:-~/.config}/guardian/config.yaml`. The destructive and
converging verbs operate on the configured host unless an explicit path is
provided.

Provider refresh and hardware probing should remain narrow host-inventory
operations when added. They should update provider-derived or machine-derived
fields only, produce reviewable diffs, and never enroll, wipe, or deploy by
themselves.
