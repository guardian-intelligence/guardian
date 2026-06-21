# talm — Talos machine-config source (guardian-mgmt)

The reproducible Talos Linux layer for the guardian-mgmt control plane. This is
the `talm` Helm-style chart that renders each node's machine config. It is the
in-repo source of truth (previously only at `~/.guardian-deploy/talm/`).

It sits **outside** the Flux path (`src/infrastructure/base/`), so Flux never
tries to apply Talos config as Kubernetes manifests. It is applied by the `talm`
binary during a cold boot, not by the cluster.

## Topology: shared L2 over a Latitude Virtual Network (no KubeSpan)

This config targets the post-rebuild topology — all control-plane nodes on one
Latitude Virtual Network (true L2), KubeSpan **removed**:

- `endpoint` / `floatingIP` = `10.8.0.250` — a Talos `Layer2VIPConfig` for the
  k8s API, possible only on shared L2 (an L2 VIP can't span the old per-node
  `/31`s). Replaces the per-node public-IP `apiServerEndpoint` hack.
- `advertisedSubnets: [10.8.0.0/24]` — kubelet nodeIP + etcd advertise ride the
  private VLAN (nodes `.11/.12/.13`).
- `certSANs` = the VIP + `api.guardianintelligence.org` (no public `/31` IPs).
- **No KubeSpan side-patch.** The old flow stacked `patch-kubespan.yaml` on every
  `talm apply`; the shared-L2 rebuild drops it entirely (removes the +80
  WireGuard tax that forced the crippled 1222 pod MTU). `cluster.discovery` stays
  `false`.

## Secrets — never committed

`secrets.yaml`, `talm.key`, `talosconfig*` are gitignored. Clean-slate rebuild
regenerates them:

```
talm gen secrets            # -> secrets.yaml + talm.key (fresh PKI, secret-zero)
```

These live only on the release runner's filesystem (`~/.guardian-deploy/talm/`).

## Rebuild usage (see docs/runbooks/cozystack-mgmt-rebuild.md for the full flow)

REBUILD GATES (attach-time values not knowable until the VLAN exists):

1. Create the Latitude Virtual Network → read back its **VID**.
2. Set `vipLink: <parent>.<VID>` in `values.yaml`, where `<parent>` is the
   physical NIC Latitude trunks the tag onto (confirm at attach — the fabric is
   `enp1s0f0`/`enp1s0f1`).
3. Per node, the generated `nodes/<n>.yaml` body overlay must bring that VLAN
   link up with a `VLANConfig` (vlanID `<VID>`, parent `<parent>`, address
   `10.8.0.1X/24`, `mtu` = the probed VLAN MTU) — replacing the old public-`/31`
   `LinkConfig`.
4. Confirm the VLAN MTU before trusting the pod MTU (1442 assumes a clean 1500):
   `ping -M do -s 1472 -c3 10.8.0.12` across two nodes.

Then: `talm template` → `talm apply` (no `--config-patch patch-kubespan.yaml`),
bootstrap etcd on the first node.
