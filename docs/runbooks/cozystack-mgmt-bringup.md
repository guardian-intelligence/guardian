# Cozystack mgmt cluster bring-up (by hand)

How to reproduce `guardian-mgmt` from cold metal: a 3-node Talos control plane
in **three different public /31 subnets** (no shared L2), meshed by KubeSpan
WireGuard, running Cozystack 1.4.4 isp-full. This is the **by-hand** procedure —
the single-node `guardian` CLI was deleted (deploy was hand-driven), and the talm
project lives in a **volatile `/tmp`** dir. Read the persistence warning first.

> ⚠️ **PKI persistence (read first).** Everything that makes the cluster
> manageable — the talm secrets (`secrets.yaml`, the cluster CA + machine
> certs) and the generated `kubeconfig` — was created under `/tmp/guardian-deploy`
> on the workstation. **`/tmp` does not survive a reboot.** Lose it and you lose
> the ability to regenerate node configs, rotate certs, or talk to the API as
> admin — the cluster becomes effectively unmanageable (recoverable only by a
> wipe + reinstall). **Persist `/tmp/guardian-deploy` to durable storage (and the
> survival-floor R2 path) before you walk away.** TODO: pick the canonical home
> (`~/.local/state/guardian/clusters/guardian-mgmt/`) and commit the move.

## Cluster facts

| node | public /31 | k8s node | role |
|---|---|---|---|
| ash-earth | 206.223.228.101 | talos-7c93e | CP, etcd bootstrap, API endpoint |
| ash-wind  | 45.250.254.119  | talos-4085f | CP |
| ash-fire  | 67.213.115.113  | talos-a63fa | CP |

Per node: 2x960GB NVMe — install `/dev/nvme0n1`, ZFS data disk `/dev/nvme1n1`.
Talos **v1.12.6**, k8s **v1.34.3**, Cozystack **1.4.4** isp-full. CNI = Kube-OVN
+ Cilium. No VIP (an L2 VIP can't span three subnets); the API endpoint is
ash-earth's IP. DNS round-robin across the three is a follow-up.

## Prerequisites

Pinned tools matching the Cozystack-1.4.4 talm-preset set (already aligned on
`main`, PR A): **talm v0.31.0**, **talosctl 1.12.6**, **kubectl 1.34.3**, **helm**.
See the working-with-guardian-infra memory for how to materialise `/tmp/gbin`.

```sh
export PATH=/tmp/gbin:$PATH
mkdir -p /tmp/guardian-deploy && cd /tmp/guardian-deploy   # ⚠️ volatile — see warning above
```

**Boot state.** Latitude's reinstall flow nominally lays down Ubuntu, but these
nodes iPXE-boot **Talos maintenance mode** directly — so **skip the boot-to-Talos
step**: the nodes already answer talosctl (insecure) on their public IPs. Confirm:

```sh
for ip in 206.223.228.101 45.250.254.119 67.213.115.113; do
  talosctl -n "$ip" -e "$ip" --insecure version --client=false   # ⏹ reports a Talos version, no node config yet
done
```

## 1 — Gather per-node facts

talm's `cozystack` preset auto-discovers each node's static /31 network and its
install disk, but verify the disks/links before templating so a wrong device
doesn't wipe the data NVMe:

```sh
for ip in 206.223.228.101 45.250.254.119 67.213.115.113; do
  echo "== $ip =="
  talosctl -n "$ip" -e "$ip" --insecure get disks      # ⏹ nvme0n1 (install) + nvme1n1 (ZFS data), both ~960GB
  talosctl -n "$ip" -e "$ip" --insecure get links       # physical NIC + its MTU (link MTU 1500)
  talosctl -n "$ip" -e "$ip" --insecure get addresses    # the static /31 + gateway
done
```

## 2 — talm init (cozystack preset) + the KubeSpan/certSANs side-patch

```sh
talm init --preset cozystack       # writes talconfig + per-node templates; auto-discovers /31 + install disk
```

The preset does NOT inject cross-subnet meshing or the multi-IP cert SANs. Apply a
**side-patch** so (a) KubeSpan WireGuard meshes the three subnets, (b) cluster
discovery lets peers find each other without a shared L2, and (c) the API cert is
valid for the public DNS name and all three node IPs (no VIP):

```yaml
# kubespan-discovery.patch.yaml — applied to every node template
machine:
  network:
    kubespan:
      enabled: true
cluster:
  discovery:
    enabled: true
---
# certSANs — the apiserver must be reachable as the DNS name AND any node IP
cluster:
  apiServer:
    certSANs:
      - api.guardianintelligence.org
      - 206.223.228.101
      - 45.250.254.119
      - 67.213.115.113
```

Set the cluster endpoint to ash-earth's IP (no VIP):

```sh
# in talconfig: endpoint = https://206.223.228.101:6443
```

> TODO: fold the side-patch into the talm project as a tracked inline patch so it
> is not re-derived by hand on the next converge.

## 3 — Template + apply per node

```sh
for node in ash-earth ash-wind ash-fire; do
  talm template -n "$node" > "$node.yaml"
  talm apply -f "$node.yaml" --insecure     # first apply is insecure (maintenance mode)
done
```

⏹ Nodes reboot into Talos installed mode, format `/dev/nvme0n1`, and come up with
KubeSpan enabled. Verify the **mesh** (this is the load-bearing check — see the MTU
bug below): use `kubespanpeerstatuses`, NOT `LinkStatus`:

```sh
talosctl -n 206.223.228.101 -e 206.223.228.101 get kubespanpeerstatuses
# ⏹ two peers, state=up, each in a different /31 — the WireGuard mesh is healthy
```

## 4 — Bootstrap etcd (node1 only) + kubeconfig

Bootstrap etcd on exactly one node (ash-earth). The other two join the quorum
across their own subnets via KubeSpan:

```sh
talosctl -n 206.223.228.101 -e 206.223.228.101 bootstrap   # ONCE, ash-earth only
talosctl -n 206.223.228.101 -e 206.223.228.101 health      # ⏹ etcd quorum forms across all 3 subnets
talm kubeconfig ./kubeconfig                                # ⚠️ persist this — see warning
export KUBECONFIG=$PWD/kubeconfig
kubectl get nodes   # ⏹ talos-7c93e / talos-4085f / talos-a63fa all Ready
```

## 5 — Install Cozystack + apply the Platform Package

```sh
helm install cozystack -n cozy-system --create-namespace \
  oci://ghcr.io/cozystack/cozystack/cozystack \
  --version 1.4.4 \
  --set cozystackOperator.disableTelemetry=true     # cozy-installer
```

Then apply the **Platform Package** CR (variant `isp-full`, carrying the Guardian
white-label branding for the dashboard). Tracked at
`src/infrastructure/base/cozystack/platform.yaml`:

```sh
kubectl apply -f src/infrastructure/base/cozystack/platform.yaml
```

⏹ Watch the HelmReleases converge — **93/93 healthy**:

```sh
kubectl get helmreleases -A    # ⏹ all READY=True (give it time; CNI + storage come up first)
```

> Note: the dashboard converges only **after Keycloak is enabled** (step 8). Until
> then expect the console HelmRelease to wait.

## 6 — Fabric MTU subnet patch (REQUIRED — do not skip)

**This is the crown-jewel fix.** Kube-OVN's default pod MTU is 1400. Cross-node
pod traffic is double-encapsulated: pod packet + **GENEVE (58)** + **KubeSpan
WireGuard (80)** = up to **1538 bytes** on a node link with MTU **1500** → large
cross-node packets are **silently dropped**. Small-packet workloads are fine, so
the cluster *looks* healthy — but anything large (e.g. OpenBao raft's cluster-port
TLS handshake) breaks. The principle: **MTU must be uniform across the whole
fabric**, sized below the link minus both encaps.

```sh
kubectl patch subnet ovn-default --type merge -p '{"spec":{"mtu":1222}}'
# join subnet too, if used:
kubectl patch subnet join --type merge -p '{"spec":{"mtu":1222}}'
kubectl get subnet -o custom-columns=NAME:.metadata.name,MTU:.spec.mtu   # ⏹ 1222
```

> ⚠️ **Durability + uniformity caveats.** (1) The Platform-Package
> `kube-ovn.mtu` value is **inert** — it did not propagate to kube-ovn; the
> *working lever* is this subnet patch. (2) **Only newly-created pods get 1222.**
> Pods that existed before the patch keep 1400 until they cycle. After patching,
> bounce stateful workloads (or the whole fabric) so the MTU is uniform. The
> KubeSpan mesh itself was healthy throughout — confirm with
> `talosctl get kubespanpeerstatuses`, never `LinkStatus`.
> TODO: find a durable, declarative home for `mtu=1222` that actually propagates.
> Note at `src/infrastructure/base/networking/`.

## 7 — LINSTOR ZFS storage

Create a ZFS-backed device pool named **`data`** on each node's second NVMe
(`/dev/nvme1n1`), from the linstor-controller:

```sh
LC="kubectl -n cozy-linstor exec deploy/linstor-controller -c linstor-controller -- linstor"
for node in talos-7c93e talos-4085f talos-a63fa; do
  $LC physical-storage create-device-pool zfs "$node" /dev/nvme1n1 --pool-name data --storage-pool data
done
$LC storage-pool list    # ⏹ pool `data` present on all 3 nodes
```

Apply the four StorageClasses (tracked at
`src/infrastructure/base/storage/storageclasses.yaml`): `local` (raw ZFS zvol,
single replica, **default**), `local-retain`, `replicated` (DRBD x3), and
`replicated-retain`. The `-retain` variants keep the PV/zvol on PVC delete.

```sh
kubectl apply -f src/infrastructure/base/storage/storageclasses.yaml
kubectl get sc    # ⏹ local (default) / local-retain / replicated / replicated-retain
```

Smoke-test the provisioner: bind a throwaway PVC on `local`, confirm a zvol
appears (`$LC resource list`), delete it.

## 8 — Keycloak + dashboard

Enable the Cozystack built-in IdP (Keycloak). The dashboard's gatekeeper
oauth2-proxy needs an OIDC issuer it can reach **internally** (no external DNS
yet), so set the internal URL:

```sh
# in the Platform Package values:
#   authentication.oidc.enabled: true
#   keycloakInternalUrl: <in-cluster keycloak svc URL>   # TODO: record exact value
kubectl apply -f src/infrastructure/base/cozystack/platform.yaml
```

⏹ The dashboard HelmRelease converges and renders the **"Guardian"** white-label.
Browser SSO login still needs **DNS + ingress** (deferred edge) — the OIDC wiring
is correct internally but the redirect/login flow is not reachable from a browser
until the edge lands.

## 9 — OpenBao HA (raft)

Deploy the Cozystack-managed OpenBAO app (`kind: OpenBAO`, `replicas: 3`, raft),
tracked at `src/infrastructure/base/openbao/openbao.yaml` (namespace `tenant-root`).
**Two things are required first or raft will not form:**

1. The **MTU patch** (step 6) — without it the raft cluster-port TLS is dropped.
2. The **apiserver egress CNP** — raft mode adds `service_registration
   "kubernetes"`, which hangs startup unable to reach the k8s API. The Cozystack
   tenant baseline (`tenant-root-egress`/`-ingress`) is **default-deny**; unlabeled
   pods get only `world` + intra-cluster egress, and the apiserver is neither.
   API egress is opt-in via `policy.cozystack.io/allow-to-apiserver: "true"`, which
   the managed app never sets. So grant it directly (tracked at
   `src/infrastructure/base/openbao/networkpolicy.yaml`):

```sh
kubectl apply -f src/infrastructure/base/openbao/networkpolicy.yaml   # CNP: openbao pods -> kube-apiserver/:6443
kubectl apply -f src/infrastructure/base/openbao/openbao.yaml
kubectl -n tenant-root get pods -l app.kubernetes.io/name=openbao   # ⏹ all 3 raft pods serving
```

### Init + unseal (PENDING)

The 3 raft pods serve, but OpenBao is **not yet initialised**. The next operator
must run `bao operator init` and hold the resulting Shamir keys (the "secret
zero") — out of band, never on the cluster. For the prod-shaped transit
auto-unseal + TLS + `retry_join` pattern (so init survives restart/restore), follow
[openbao-tracer.md](openbao-tracer.md) — the listener config, the
`leader_ca_cert_file` and `global.tlsDisable:false` gotchas, the periodic seal
token, and the idempotency guards all carry over.

```sh
# scaffold (adapt to the tracer's transit/TLS overlay before prod):
kubectl -n tenant-root exec -i openbao-0 -- bao operator init   # ⏹ record keys OUT OF BAND — secret zero
```

## Done / not done

Bring-up through OpenBao **serving** is reproducible by this runbook. Remaining:
OpenBao **init/unseal**, persisting the `/tmp` PKI, DNS/ingress for browser SSO,
and a durable home for the MTU value. See
[../cozystack-mgmt-handoff.md](../cozystack-mgmt-handoff.md) for status against the
original plan.
