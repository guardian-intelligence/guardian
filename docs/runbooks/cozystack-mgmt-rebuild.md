# Runbook: guardian-mgmt clean-slate rebuild (shared-L2 VLAN)

Reproducible cold boot of the guardian-mgmt control plane onto a Latitude
Virtual Network (true L2). This is the canonical bring-up runbook;
`src/infrastructure/bootstrap/guardian-mgmt/` +
`src/infrastructure/bootstrap/cloudflare-dns/` + `src/infrastructure/base/` +
`src/infrastructure/talm/` + this doc are the complete non-secret desired-state
source.

Generated operator state lives under
`${XDG_STATE_HOME:-$HOME/.local/state}/guardian/clusters/guardian-mgmt/` and is
safe to wipe. It may contain kubeconfigs, Talos/Talm PKI, rendered machine
configs, OpenBao init output, and operation evidence. It must not contain the
only copy of infrastructure intent.

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
| Private subnet | `10.8.0.0/24` — nodes `.11/.12/.13`, API VIP `.250`, reserve `.200–.240` for private MetalLB services |
| MTU | VLAN path **1420** (measured) → pod **1362** |
| Data disk | `/dev/nvme1n1` (OS on `nvme0n1`) — uniform across nodes |
| Talos / k8s / Cozystack | v1.12.6 / v1.34.3 / 1.4.x isp-full |

Local paths: generated kubeconfig and Talm state live under
`${XDG_STATE_HOME:-$HOME/.local/state}/guardian/clusters/guardian-mgmt/`.
Latitude Bearer material comes from the operator secret store or a temporary
environment/file for the session. Pinned tools come from this repo through
Bazel/aspect runfiles, not a hand-maintained scratch bin directory.

---

## Phase 0 — repo is correct (done before any metal moves)
The branch `infra/vlan-rebuild-iac` carries all declarative changes (see the PR
change-list). MTU = 1362 (validated); `vipLink: enp1s0f0.2140` set. Review and
approve — but hold the merge per the timing note above.

No generated YAML is authoritative. If a value is required to rebuild the
cluster, capture it in `src/infrastructure/talm/`, `src/infrastructure/base/`,
or the checked-in host/cluster inventory before touching the metal.

## Phase 1 — provision metal + VLAN
**The VLAN already exists (VID 2140) and L2 is validated** — see the confirmed
section above. Adopt and validate the Latitude substrate through OpenTofu first:
```
export LATITUDESH_AUTH_TOKEN=...
aspect infra init
aspect infra adopt-known
aspect infra plan
```

`src/infrastructure/bootstrap/guardian-mgmt/` is authoritative for project,
server, and VLAN identity. VLAN assignment resources require provider assignment
IDs before they can be imported; see that directory's README before running any
`apply`.

If the substrate truly has to be recreated from scratch, the API shape is:
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
export GUARDIAN_CLUSTER=guardian-mgmt
export GUARDIAN_STATE="${XDG_STATE_HOME:-$HOME/.local/state}/guardian/clusters/${GUARDIAN_CLUSTER}"
rm -rf "${GUARDIAN_STATE}"
mkdir -p "${GUARDIAN_STATE}"
cp -a src/infrastructure/talm "${GUARDIAN_STATE}/talm"
cd "${GUARDIAN_STATE}/talm"
talm gen secrets    # fresh secrets.yaml + talm.key
```

## Phase 3 — fill the VLAN gates, then talm apply
1. Confirm `src/infrastructure/talm/values.yaml` `vipLink: <parent>.<VID>` (confirm
   `<parent>`: the NIC Latitude tagged — `enp1s0f0`/`enp1s0f1`).
2. Render the node bodies into `${GUARDIAN_STATE}/talm/nodes/` from checked-in
   inputs. Treat rendered files as generated output: if a required value is
   missing, fix the repo input and rerender rather than hand-editing the output.
   Each node body must include the VLAN link:
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
networkpolicy, root/dev/gamma tenant declarations, and the company-site
deployment envelope.

Postgres and ClickHouse also declare R2 backup plumbing. Their releases will
not become fully healthy until `tenant-root/guardian-r2-db-backups` exists with
the keys declared in `src/infrastructure/inventory/guardian-mgmt.json`. Seed
that Secret during secret-zero bring-up, then let Flux retry reconciliation.

## Phase 5.5 — adopt and plan DNS
Cloudflare DNS is declared in `src/infrastructure/bootstrap/cloudflare-dns/` from
the shared inventory. Adopt existing records before changing DNS:
```
export CLOUDFLARE_API_TOKEN=...
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_ENDPOINT_URL_S3=...
aspect infra dns-init
aspect infra dns-adopt-known
aspect infra dns-plan
```

Review the plan before `apply`. Moving the apex or
`oci.guardianintelligence.org` changes public traffic routing.

## Phase 6 — re-seed secret-zero
OpenBao init + unseal (3-replica raft on `replicated-retain`); persist unseal
key + root token under `${GUARDIAN_STATE}/secret-zero/` (filesystem perms only,
never git) and back them up through the survival-floor process. Re-seed Keycloak
realm/clients. Re-mint any Transit / release-judge credentials. Seed the
temporary Kubernetes delivery Secret `tenant-root/guardian-r2-db-backups` from
the operator secret source until the OpenBao projection controller owns that
contract.

## Phase 7 — verify (end-to-end, like production)
Use `docs/runbooks/management-evidence.md` for the repo-owned command surface
and report requirements. The quick acceptance checks here are the minimum live
signals before filling the checked-in reports.

- etcd: 3 voting members over the VLAN, equal RAFT INDEX.
- `kubectl get nodes` all Ready; VIP `10.8.0.250:6443` answers.
- **Pod-level** large-packet probe across nodes (not just host VLAN) — a fresh
  pod resolves DNS, reaches a ClusterIP, and pushes a >1400B flow cross-node
  with no silent drop. This is the MTU truth test.
- `linstor resource list`: OpenBao volumes are 3-way DRBD.
- Dashboard reachable via the node-public-IP ingress exposure; private
  LoadBalancer services allocate from the MetalLB/L2 VLAN range.
- Confirm in ClickHouse/observability that traces/logs flow.

---

## What is declarative
- `data` ZFS pool → `storage/linstor-satellite-config.yaml`.
- Latitude project, servers, VLAN, and VLAN assignments →
  `src/infrastructure/bootstrap/guardian-mgmt/`.
- Cloudflare public DNS records →
  `src/infrastructure/bootstrap/cloudflare-dns/`.
- Platform stateful HA → default `replicated` SC + OpenBao `replicated-retain`.
- Dev/gamma/prod stage intent →
  `src/environments/{dev,gamma,prod}/environment.yaml`; dev and gamma become
  child tenants that inherit root tenant services.
- Company site artifact and deployment →
  `src/products/company/site/` and
  `src/infrastructure/base/products/company-site.yaml`.
- Talos/VLAN/VIP topology → `src/infrastructure/talm/`.
- API exposure → Talos L2 VIP; public HTTP(S) exposure → node public IPs;
  private LoadBalancer exposure → MetalLB L2.

Still imperative by nature (documented here, not in Flux): boot-to-Talos,
`talm gen secrets`, OpenBao init/unseal.
