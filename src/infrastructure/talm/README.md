# talm — Talos machine-config source (guardian-mgmt)

The reproducible Talos Linux layer for the guardian-mgmt control plane: the
`talm` Helm-style chart that renders each node's machine config. It is the
in-repo source of truth.

It sits **outside** the Flux path (`src/infrastructure/base/`), so Flux never
applies Talos config as Kubernetes manifests. The `talm` binary applies it
during a cold boot, not the cluster.

## Topology

All control-plane nodes share one Latitude Virtual Network (VID 2140) for true
L2/ARP:

- `endpoint` / `floatingIP` = `10.8.0.250` — a Talos `Layer2VIPConfig` fronts
  the k8s API.
- `vipLink: enp1s0f0.2140` — the VLAN child link the VIP pins to; the per-node
  overlay brings it up with a `VLANConfig`.
- `advertisedSubnets: [10.8.0.0/24]` — kubelet nodeIP + etcd advertise ride the
  private VLAN (nodes `.11/.12/.13`).
- `certSANs` = the VIP, `api.guardianintelligence.org`, and each node's public IP.

## Secrets - never committed

`secrets.yaml`, `talm.key`, `talosconfig*` are gitignored and live only on the
operator state path:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/guardian/clusters/<cluster>/talm/
```

That directory is generated from this checked-in chart and is safe to delete
between rebuilds. If a value is needed to reproduce the cluster, it belongs in
this repo, not in the generated state directory. A clean-slate rebuild
regenerates the secret-zero material:

```
talm gen secrets            # -> secrets.yaml + talm.key (fresh PKI, secret-zero)
```

## Usage

See **docs/runbooks/cozystack-mgmt-rebuild.md** for the full cold-boot flow.
