# Runbook: guardian-mgmt clean-slate rebuild (shared-L2 VLAN)

Reproducible cold boot of the guardian-mgmt control plane onto a Latitude
Virtual Network (true L2). This is the canonical bring-up runbook;
`src/infrastructure/base/` + `src/infrastructure/talm/` + this doc are the
complete source of truth.

**Clean slate:** nothing is preserved. secret-zero (OpenBao unseal/root,
Keycloak admin) is re-minted; no data restore.

---

## L2 + MTU: CONFIRMED ON HARDWARE (2026-06-21)

Validated empirically before committing: created VID 2140 on the live mgmt nodes
and tested host-netns VLAN interfaces.
- **L2 works** — ping across the segment at 0.2ms; ARP resolves. (Latitude's
  `failed_connection` assignment status is benign — its own health check,
  failing only because the OS side wasn't configured. Test with a real packet.)
- **VLAN path MTU = 1420** (`tracepath`), not 1500 — the fabric clamps tagged
  VLANs ~80B down; jumbo unavailable. So **pod MTU = 1420 − 58 GENEVE = 1362**
  (committed in `subnet-mtu.yaml`). Nested tenant clusters: `1362 − 50 = 1312`.
- **VLAN rides `enp1s0f0`** (the secondary/private NIC; public IP is on
  `enp1s0f1`). Sub-interface `enp1s0f0.2140`.

---

## ⚠️ Merge timing (read before merging the rebuild PR)

The live cluster's Flux reconciles `main`. Keep the rebuild changes on the
branch / open PR — do NOT merge to `main` until the rebuild cutover, when the
target cluster is provisioned (or its Flux is quiesced). Repo-first means
"reviewed and correct," not "merged onto the live cluster."

---

## Inventory (confirmed 2026-06-21)

| Thing | Value |
|---|---|
| Latitude project | `proj_R82A0yqmd06mM` (guardian-mgmt) |
| Site / facility | `ASH` / DEFT IAD2 (all nodes here) |
| Servers | ash-water `sv_8mop5gZo8Njxv`, ash-wind `sv_nPRbajqEB5koM`, ash-earth `sv_vAPXaMxKM5epz` |
| Virtual Network | `vlan_8mop5gkpP5jxv` — **VID 2140**, ASH; rides NIC `enp1s0f0` (sub-iface `enp1s0f0.2140`) |
| Private subnet | `10.8.0.0/24` — nodes `.11/.12/.13`, API VIP `.250`, reserve `.200–.240` for MetalLB |
| MTU | VLAN path **1420** (measured) → pod **1362** |
| Data disk | `/dev/nvme1n1` (OS on `nvme0n1`) — uniform across nodes |
| Talos / k8s / Cozystack | v1.12.6 / v1.34.3 / 1.4.x isp-full |

Tokens/paths: `~/.guardian-deploy/kubeconfig`, talm at `~/.guardian-deploy/talm/`
(source vendored at `src/infrastructure/talm/`), Latitude Bearer at
`/tmp/latitude.token`, pinned tools in `/tmp/gbin`.

---

## Phase 0 — repo is correct (done before any metal moves)
The branch `infra/vlan-rebuild-iac` carries all declarative changes (see the PR
change-list). MTU = 1362 (validated); `vipLink: enp1s0f0.2140` set. Review and
approve — but hold the merge per the timing note above.

## Phase 1 — provision metal + VLAN
**The VLAN already exists (VID 2140) and L2 is validated** — see the confirmed
section above. To recreate from scratch:
1. **Create the Virtual Network** (nested JSON:API; `site` is the slug `ASH`):
   ```
   POST https://api.latitude.sh/virtual_networks
   { "data": { "type": "virtual_network", "attributes": {
       "description": "guardian-mgmt L2 fabric",
       "project": "proj_R82A0yqmd06mM", "site": "ASH" } } }
   ```
   Then `GET /virtual_networks` → record the `id` (`vlan_…`) and **`vid`**
   (auto-assigned). Assignment body is ALSO nested:
   `{ "data": { "type": "virtual_network_assignment", "attributes":
   { "server_id": "<sv_…>", "virtual_network_id": "<vlan_…>" } } }`. The
   `failed_connection` status it returns is benign (test L2 with a real packet).
2. **Assign all 3 servers** (live-attach, no reinstall):
   ```
   POST https://api.latitude.sh/virtual_networks/assignments
   { "server_id": "<sv_…>", "virtual_network_id": "<vlan_…>" }
   ```
3. **CONFIRM THE VLAN MTU.** Bring the tagged sub-iface up on two nodes with
   temp IPs and probe the path with `tracepath`/DF-set pings to find the largest
   clearing packet. The fabric clamps tagged VLANs to a **1420** path. Set
   `POD_MTU = VLAN_MTU − 58` (= **1362**) in `subnet-mtu.yaml` and the node
   `VLANConfig`.

## Phase 2 — boot to Talos + fresh secret-zero
PXE/`boot-to-talos` each node into Talos maintenance over its existing public
NIC (the private fabric isn't up yet). Then:
```
cd ~/.guardian-deploy/talm && talm gen secrets    # fresh secrets.yaml + talm.key
```

## Phase 3 — fill the VLAN gates, then talm apply
1. Set `src/infrastructure/talm/values.yaml` `vipLink: <parent>.<VID>` (confirm
   `<parent>`: the NIC Latitude tagged — `enp1s0f0`/`enp1s0f1`).
2. Generate + patch each node body with the VLAN link:
   ```yaml
   apiVersion: v1alpha1
   kind: VLANConfig
   name: <parent>.<VID>
   vlanID: <VID>
   parent: <parent>
   mtu: <VLAN_MTU>
   addresses: [ 10.8.0.1X/24 ]      # .11 / .12 / .13
   ```
3. Apply:
   ```
   talm template -f nodes/<n>.yaml --kubernetes-version 1.34.3
   talm apply    -f nodes/<n>.yaml --kubernetes-version 1.34.3 --nodes 10.8.0.1X --endpoints 10.8.0.1X
   ```
   Bootstrap etcd on the first node; wait for the VIP `10.8.0.250:6443` to answer.

## Phase 4 — Cozystack
Install the `isp-full` Cozystack platform against the VIP. `subnet-mtu.yaml`
pins pod MTU to 1362 (the 1420 VLAN path − 58 GENEVE).

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

## What is declarative
- `data` ZFS pool → `storage/linstor-satellite-config.yaml`.
- Platform stateful HA → default `replicated` SC + OpenBao `replicated-retain`.
- Talos/VLAN/VIP topology → `src/infrastructure/talm/`.
- API exposure → L2 VIP + MetalLB.

Still imperative by nature (documented here, not in Flux): Latitude provisioning,
boot-to-Talos, `talm gen secrets`, OpenBao init/unseal.
