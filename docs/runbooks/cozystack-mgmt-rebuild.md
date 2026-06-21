# Runbook: guardian-mgmt clean-slate rebuild (shared-L2 VLAN, no KubeSpan)

Reproducible cold boot of the guardian-mgmt control plane onto a Latitude
Virtual Network (true L2), with KubeSpan removed. This is the canonical bring-up
runbook; it replaces the retired cross-subnet `/31` + KubeSpan + MTU-1222
procedure (whose bespoke fabric was the root cause of the layered MTU pain).

**Why a rebuild, not in-place repair:** an in-place CP node swap diverges the
cluster's node-pinned stateful systems (etcd / OVN raft / LINSTOR / CNPG /
OpenBao raft) from declared state, forcing imperative drift-repair. A rebuild
regenerates all of it from code. This runbook + `src/infrastructure/base/` +
`src/infrastructure/talm/` are the complete source of truth.

**Clean slate:** nothing is preserved. secret-zero (OpenBao unseal/root,
Keycloak admin) is re-minted; no data restore.

---

## The one load-bearing unknown: VLAN MTU

Latitude does not document the Virtual Network MTU. The repo commits the
**assumption of a clean 1500** L2 → pod MTU **1442** (`1500 − 58` kube-ovn
GENEVE; Cilium chained adds no extra encap; KubeSpan's +80 is gone). This MUST
be confirmed at attach (Phase 1.3). If the VLAN clamps below 1500, change
`networking/subnet-mtu.yaml` (both `mtu:`) and the per-node `VLANConfig` mtu to
`VLAN_MTU − 58` before installing Cozystack.

---

## ⚠️ Merge timing (read before merging the rebuild PR)

The live cluster's Flux reconciles `main`. This repo now describes the
**post-rebuild VLAN topology**; applying it to the *current* cross-subnet +
KubeSpan cluster would break it (VIP endpoint with no VIP, pod MTU 1442 > the
1362 KubeSpan ceiling → silent drops). Therefore: **keep the rebuild changes on
the branch / open PR — do NOT merge to `main` until the rebuild cutover**, when
the old cluster is being replaced (or its Flux is quiesced). Repo-first means
"reviewed and correct," not "merged onto the live cluster."

---

## Inventory (confirmed 2026-06-21)

| Thing | Value |
|---|---|
| Latitude project | `proj_R82A0yqmd06mM` (guardian-mgmt) |
| Site / facility | `ASH` / DEFT IAD2 (all nodes here) |
| Servers | ash-water `sv_8mop5gZo8Njxv`, ash-wind `sv_nPRbajqEB5koM`, ash-earth `sv_vAPXaMxKM5epz` |
| Private subnet | `10.8.0.0/24` — nodes `.11/.12/.13`, API VIP `.250`, reserve `.200–.240` for MetalLB |
| Data disk | `/dev/nvme1n1` (OS on `nvme0n1`) — uniform across nodes |
| Talos / k8s / Cozystack | v1.12.6 / v1.34.3 / 1.4.x isp-full |

Tokens/paths: `~/.guardian-deploy/kubeconfig`, talm at `~/.guardian-deploy/talm/`
(source vendored at `src/infrastructure/talm/`), Latitude Bearer at
`/tmp/latitude.token`, pinned tools in `/tmp/gbin`.

---

## Phase 0 — repo is correct (done before any metal moves)
The branch `infra/vlan-rebuild-iac` carries all declarative changes (see the PR
change-list). MTU committed as the 1442 assumption; `vipLink` blank pending the
VID. Review and approve — but hold the merge per the timing note above.

## Phase 1 — provision metal + VLAN  ·  the only live-confirm gate
1. **Create the Virtual Network** (nested JSON:API; `site` is the slug `ASH`):
   ```
   POST https://api.latitude.sh/virtual_networks
   { "data": { "type": "virtual_network", "attributes": {
       "description": "guardian-mgmt L2 fabric",
       "project": "proj_R82A0yqmd06mM", "site": "ASH" } } }
   ```
   Then `GET /virtual_networks?filter[project]=proj_R82A0yqmd06mM` → record the
   `id` (`vlan_…`) and **`vid`** (auto-assigned).
2. **Assign all 3 servers** (live-attach, no reinstall):
   ```
   POST https://api.latitude.sh/virtual_networks/assignments
   { "server_id": "<sv_…>", "virtual_network_id": "<vlan_…>" }
   ```
3. **CONFIRM THE VLAN MTU.** Bring the tagged sub-iface up on two nodes with
   temp IPs and probe with DF set:
   ```
   ping -M do -s 1472 -c3 10.8.0.12     # 1472 = 1500 − 28; success ⇒ L2 clears 1500
   ```
   Record the result. Success ⇒ pod MTU 1442 stands. Failure ⇒ bisect, set
   `POD_MTU = VLAN_MTU − 58`, and edit `subnet-mtu.yaml` + the node `VLANConfig`.

## Phase 2 — boot to Talos + fresh secret-zero
PXE/`boot-to-talos` each node into Talos maintenance over its existing public
NIC (the private fabric isn't up yet). Then:
```
cd ~/.guardian-deploy/talm && talm gen secrets    # fresh secrets.yaml + talm.key
```

## Phase 3 — fill the VLAN gates, then talm apply (KubeSpan dropped)
1. Set `src/infrastructure/talm/values.yaml` `vipLink: <parent>.<VID>` (confirm
   `<parent>`: the NIC Latitude tagged — `enp1s0f0`/`enp1s0f1`).
2. Generate + patch each node body with the VLAN link (replacing the public-/31
   `LinkConfig`):
   ```yaml
   apiVersion: v1alpha1
   kind: VLANConfig
   name: <parent>.<VID>
   vlanID: <VID>
   parent: <parent>
   mtu: <VLAN_MTU>
   addresses: [ 10.8.0.1X/24 ]      # .11 / .12 / .13
   ```
3. Apply **without** the KubeSpan side-patch (it is deleted from the flow):
   ```
   talm template -f nodes/<n>.yaml --kubernetes-version 1.34.3
   talm apply    -f nodes/<n>.yaml --kubernetes-version 1.34.3 --nodes 10.8.0.1X --endpoints 10.8.0.1X
   ```
   Bootstrap etcd on the first node; wait for the VIP `10.8.0.250:6443` to answer.

## Phase 4 — Cozystack
Install the `isp-full` Cozystack platform against the VIP. With clean 1500 L2,
kube-ovn calibrates pod MTU to 1442 (subnet-mtu.yaml then pins it explicitly).

## Phase 5 — Flux reconciles the repo
Merge the rebuild PR (now safe — old cluster gone), then:
```
kubectl apply -f src/infrastructure/base/flux/sync.yaml
```
Flux reconciles platform.yaml, storageclasses (default = `replicated`),
`linstor-satellite-config.yaml` (Piraeus auto-prepares the `data` zpool from
`/dev/nvme1n1` — no hand `create-device-pool`), openbao (`replicated-retain`),
networkpolicy.

## Phase 6 — re-seed secret-zero
OpenBao init + unseal (3-replica raft on `replicated-retain`); persist unseal
key + root token to `~/.guardian-deploy/` (filesystem perms only, never git).
Re-seed Keycloak realm/clients. Re-mint any Transit / release-judge credentials.

## Phase 7 — verify (end-to-end, like production)
- etcd: 3 voting members over the VLAN, equal RAFT INDEX.
- `kubectl get nodes` all Ready; VIP `10.8.0.250:6443` answers.
- **Pod-level** large-packet probe across nodes (not just host VLAN) — a fresh
  pod resolves DNS, reaches a ClusterIP, and pushes a >1400B flow cross-node
  with no silent drop. This is the MTU truth test.
- `linstor resource list`: OpenBao volumes are 3-way DRBD.
- Dashboard reachable via the MetalLB/L2 exposure.
- Confirm in ClickHouse/observability that traces/logs flow.

---

## What is now declarative (vs. the old imperative glue)
- `data` ZFS pool → `storage/linstor-satellite-config.yaml` (was a per-node hand
  command).
- Platform stateful HA → default `replicated` SC + OpenBao `replicated-retain`
  (was `local`, which stranded replicas on node loss).
- Talos/VLAN/VIP topology → `src/infrastructure/talm/` (was only on the runner).
- API exposure → L2 VIP + MetalLB (was the `externalIPs`/no-MetalLB hack).

Still imperative by nature (documented here, not in Flux): Latitude provisioning,
boot-to-Talos, `talm gen secrets`, OpenBao init/unseal.
