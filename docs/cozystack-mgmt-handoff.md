# Cozystack mgmt cluster — handoff

State as of 2026-06-21. The Guardian management cluster (`guardian-mgmt`) is up
and running Cozystack 1.4.4. This is an honest handoff: the bring-up **deviated
from the planned CLI-driven flow** — it was done by hand because the single-node
`guardian` CLI (and `src/clusters`, `src/hosts`, `src/environments`) were deleted
this session. Reproduce via the runbook, not the CLI.

> **Update 2026-06-21 (follow-on session).** Several "not yet done" items below are
> now done and verified live:
> - **PKI persisted** out of volatile `/tmp` → `~/.guardian-deploy/` (0700). Encrypted
>   off-box backup is the remaining gap.
> - **OpenBao initialized + unsealed** — 3/3 raft voters; secret-zero in
>   `~/.guardian-deploy/openbao-init.json`.
> - **Dashboard exposed at `https://dashboard.guardianintelligence.org`** with a real
>   Let's Encrypt cert and working **Keycloak browser SSO** (user `shovon` in
>   `cozystack-cluster-admin`). Exposure is the cross-subnet `externalIPs` path
>   (`publishing.exposure: externalIPs` + node IPs; the `root` tenant patched
>   `ingress: true`; wildcard `*.guardianintelligence.org` → the 3 node IPs on
>   Cloudflare). **kube-apiserver OIDC** wired via talm `oidcIssuerUrl` (no-reboot,
>   KubeSpan-patch-stacked, k8s pinned 1.34.3). See memory `cozystack-mgmt-dashboard-sso`.
> - Remaining from the plan: the **PR4 KubeVirt-on-zvol GATE** (still unproven), PR5+
>   tenant clusters, Track 2 worker pool, and **wiring Flux to reconcile `base/`**.

## 1 — What's been done

A **3-node Talos control plane in three different public /31 subnets** (no shared
L2), meshed by KubeSpan WireGuard, with Cozystack 1.4.4 isp-full on top:

- ash-earth `206.223.228.101` (talos-7c93e, etcd bootstrap + API endpoint),
  ash-wind `45.250.254.119` (talos-4085f), ash-fire `67.213.115.113` (talos-a63fa).
- Talos v1.12.6, k8s v1.34.3, Cozystack 1.4.4. CNI = Kube-OVN + Cilium. No VIP
  (L2 can't span subnets); endpoint = ash-earth IP. etcd quorum formed across all
  three subnets. **93/93 HelmReleases healthy.**

Delivered and healthy:

- **Dashboard**, white-labelled **"Guardian"** (branding in the Platform Package).
- **Keycloak** (Cozystack built-in IdP) enabled via `authentication.oidc.enabled`
  + `keycloakInternalUrl` so the dashboard oauth2-proxy works without external DNS.
  Browser SSO still needs DNS/ingress (deferred edge).
- **LINSTOR ZFS storage**: `data` pool on each node's `/dev/nvme1n1`; four
  StorageClasses (`local` default / `local-retain` / `replicated` / `replicated-retain`).
- **OpenBao HA** (managed OpenBAO app, 3-pod raft, namespace `tenant-root`): all 3
  pods serve. **Init/unseal still pending** (secret zero not yet held).

### The two bug fixes (the crown jewels)

1. **Fabric MTU.** Kube-OVN pod MTU 1400 + GENEVE (58) + KubeSpan WireGuard (80) =
   up to 1538 > 1500 link MTU → large cross-node packets silently dropped. Small
   traffic was fine so the cluster looked healthy; OpenBao raft's cluster-port TLS
   broke. Fix: `kubectl patch subnet ovn-default` → `mtu: 1222`. MTU must be
   **uniform** across the fabric. The KubeSpan mesh was healthy throughout (verify
   via `kubespanpeerstatuses`, not `LinkStatus`).
2. **Tenant API egress.** Raft adds `service_registration "kubernetes"`, which
   hangs startup unable to reach the k8s API. The Cozystack tenant baseline is
   **default-deny**; apiserver egress is opt-in via a label the managed app never
   sets. Fix: a CiliumNetworkPolicy selecting openbao pods → kube-apiserver:6443.

Detail: [runbooks/cozystack-mgmt-bringup.md](runbooks/cozystack-mgmt-bringup.md)
(reproduction) and [runbooks/openbao-tracer.md](runbooks/openbao-tracer.md) (the
prod-shaped OpenBao seal/TLS/HA pattern to apply at init). IaC committed this
session under `src/infrastructure/base/`: `openbao/`, `cozystack/platform.yaml`,
`storage/storageclasses.yaml`, `networking/` (MTU note). Version alignment to the
Cozystack-1.4.4 preset set (Talos v1.12.6 / k8s 1.34.3 / kubectl 1.34.3 /
talosctl 1.12.6) landed earlier as PR A, already on `main`.

## 2 — Progress against the original plan

Track 1 = control plane; Track 2 = worker pool. The bring-up substantially
covered PR1–PR3 **by hand** (not via the deleted CLI). The **PR4 GATE is unproven
and still gates the entire tenant-cluster topology** — nothing downstream of it can
be trusted until the spike passes.

| PR | scope | status |
|---|---|---|
| PR1 | define mgmt cluster config | **Substantially done, by-hand.** Config lives in volatile talm `/tmp`, not a tracked CLI artifact. |
| PR2 | bring up 3-node Talos CP | **Done, by-hand.** 3 nodes, KubeSpan mesh, etcd quorum across 3 subnets. |
| PR3 | Cozystack + ZFS storage | **Done, by-hand.** 93/93 HelmReleases; LINSTOR ZFS + 4 SCs; dashboard + Keycloak. |
| PR4 | **GATE** KubeVirt VM on a real zvol (boot/IO/reboot-survival, snapshot-clone vs raw zfs clone) — go/no-go for tenant-cluster topology | **NOT DONE.** Still gates everything below. |
| PR5 | dev as a tenant cluster (tenant-dev + nested k8s + OpenBao + ESO) | **NOT DONE** (blocked on PR4 gate). |
| PR6 | spike bare-metal worker onboarding (Ironic/Tinkerbell/Rancher vs Latitude's no-PXE/IPMI reality) | **NOT STARTED.** |
| PR7 | worker node Ubuntu24 + native ZFS joined as `pool=worker` | **NOT STARTED.** |
| PR8 | QEMU warm pool (savevm/loadvm + zvol-clone CoW, sub-second sandbox spin-up — the moat) | **NOT STARTED.** |

Plus, inside PR3's footprint: **OpenBao init/unseal remains** (3 pods serve, but
not initialised — secret zero not yet generated/held).

## 3 — Open decisions and risks

- **/tmp PKI (highest urgency).** The talm secrets and admin `kubeconfig` live in
  volatile `/tmp/guardian-deploy`. A reboot loses the ability to manage the cluster
  (regen configs, rotate certs, admin API) — recoverable only by wipe + reinstall.
  **Persist before anything else.** TODO: canonical home + survival-floor copy.
- **MTU durability + uniformity.** The Platform-Package `kube-ovn.mtu` value is
  inert (didn't propagate); the only working lever is the `subnet mtu=1222` patch,
  which is imperative and not declaratively durable. And only **new** pods get
  1222 — pre-existing pods stay at 1400 until they cycle, so the fabric is
  non-uniform until everything bounces. Needs a durable declarative fix + a fabric
  cycle.
- **Rebuild CLI vs stay runbook-driven.** The single-node `guardian` CLI and the
  `src/clusters,hosts,environments` trees were deleted; this deploy was by-hand. A
  multi-node CLI does not exist. **Decision pending:** rebuild a multi-node CLI, or
  stay runbook-driven (the runbook is the current source of truth either way).
- **Edge / DNS for SSO.** Keycloak OIDC is wired internally and the dashboard
  converges, but browser SSO login needs DNS + ingress. Deferred to the edge work
  (topology.md / gateway.md): the /31s are routed point-to-point, so multi-node
  ingress is BGP or DNS multi-A, not L2 announcement.
- **PR4 gate unproven.** The KubeVirt-on-zvol go/no-go has not run. The whole
  tenant-cluster topology (PR5 onward) is **architecturally unvalidated** until it
  passes — treat downstream plans as provisional.
- **OpenBao init/unseal.** Pods serve but the vault is uninitialised; secret zero
  isn't held. Init per the transit/TLS/`retry_join` pattern in openbao-tracer.md.
