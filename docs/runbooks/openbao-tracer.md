# OpenBao tracer bullet: prod + DR paths on the dev node

A *tracer bullet*: the thinnest end-to-end slice — **one** real dev-scoped secret
travelling OpenBao → ESO → a workload, surviving a full wipe-restore — run under
**prod-shaped** seal/TLS/HA so the seams that will matter in production are
proven now, not discovered later. Every least-privilege and idempotency claim is
closed by an explicit **negative test** (the thing that MUST fail).

Customer-grade: copy-paste with expected outcomes. Execute stage by stage; the
first-run results are recorded at the bottom. Run on `guardian-nonprod` /
`ash-bm-004`. **This runbook has the corrections from the first run (2026-06-19)
folded in** — see "Corrections" in the record section for what the initial draft
got wrong.

## Single-node fidelity caveat (read first)

This validates **configuration correctness**, not failure-domain resilience.
Three raft replicas on one node prove `retry_join` + auto-unseal-on-join wiring;
they do NOT prove HA survives a node loss (same kernel, same disk). TLS uses a
cert-manager *selfsigned* serving cert (prod uses the internal CA). Storage is
LINSTOR/DRBD on a ZFS-thin pool named `data` — volume state is visible via
`linstor` from the linstor-controller pod, NOT `zfs list` (the node is SSH-less).
Where the tracer can't reach prod fidelity it says so — no silent gaps.

## Risk coverage matrix

| # | High-risk item | Stage | Closing assertion (incl. negative test) |
|---|---|---|---|
| 1 | Seal/init idempotency — never regenerate key/root over live raft | 3, 6 | Re-apply + restart + 2nd `init` → data intact, `already initialized` error |
| 2 | Bao PVC `reclaimPolicy: Retain` (single disk, no replica) | 0, 6 | Delete PVC → zvol + data survive (Released, not gone) |
| 3 | Dev tokens least-privilege (TLS-off, wiped box) | 4 | ESO role reads only its path; CF token rejected outside its one zone |
| 4 | Commit TLS-on as base; dev disables via overlay | 2 | Main Bao listener serves TLS; `tls_disable` lives only in the dev overlay |
| 5 | Scope the snapshot token (snapshot-only, not root) | 5 | Snapshot token CAN snapshot, CANNOT `kv get` / read `sys` |
| 6 | `retry_join` for prod HA (1→3, not replicas alone) | 2 | 3 pods form quorum + each auto-unseals via transit on join |

## Preamble (controller env)

```sh
export KUBECONFIG=~/.local/state/guardian/clusters/guardian-nonprod/talm/kubeconfig
export PATH=/tmp/gbin:$PATH          # pinned talm/talosctl/kubectl/helm (see the working-with-guardian-infra notes)
set -a; source secret.env; set +a    # gitignored: cloudflare_*, R2 trio (see age note below)
# bao runs inside the pod. Main Bao serves TLS; transit Bao is plain http (internal):
bao()  { kubectl -n openbao exec -i openbao-0 -- env BAO_ADDR=https://127.0.0.1:8200 BAO_SKIP_VERIFY=true bao "$@"; }
baoT() { kubectl -n openbao exec -i openbao-transit-0 -- env BAO_ADDR=http://127.0.0.1:8200 bao "$@"; }
# GOTCHA: after init, the main Bao pods carry the low-priv transit seal token as
# BAO_TOKEN env, which OVERRIDES `bao login`. Admin calls MUST inject the root
# token explicitly. Record the root from `operator init`, then:
#   export BAO_ROOT=<main Bao root token>
baoR() { kubectl -n openbao exec -i openbao-0 -- env BAO_ADDR=https://127.0.0.1:8200 BAO_SKIP_VERIFY=true BAO_TOKEN="$BAO_ROOT" bao "$@"; }
```

**age for the snapshot upload (Stage 5):** the survival-floor mechanism encrypts
with a recipient (public key) and keeps the **identity** in the operator's sops
store — never on the cluster, never in R2. `secret.env` currently carries only the
R2 trio, **not** an `age_recipient`; add `age_recipient=age1...` to `secret.env`
before Stage 5's upload leg can run. See [survival-floor.md](survival-floor.md).

---

## Stage 0 — Storage: a `Retain` StorageClass (irons #2)

Goal: OpenBao data must survive PVC deletion. The default `local` SC is
`reclaimPolicy: Delete` — wrong for a stateful authority on a single disk. `local`
stays the cluster default; `openbao-retain` is explicitly requested by both Bao
StatefulSets.

```sh
kubectl apply -f - <<'YAML'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: {name: openbao-retain}
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: data
  linstor.csi.linbit.com/placementCount: "1"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "false"
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
reclaimPolicy: Retain
YAML
```

⏹ **Expect:** `kubectl get sc openbao-retain` shows `RECLAIMPOLICY  Retain`.

---

## Stage 1 — Transit seal provider (prod seal path; sets up #1, #6)

Goal: stand up the dedicated **transit OpenBao** that auto-unseals the main Bao —
the self-hosted-KMS pattern prod uses. The transit Bao itself uses the dev static
toy key (agent-operable); in prod its custody hardens, the topology does not.

1. Apply [openbao-transit.yaml](openbao-transit.yaml) (single replica, static toy
   seal, `openbao-retain`, `tls_disable=1` — internal-only seal provider; derived
   from `openbao.yaml` with `releaseName: openbao-transit`). It comes up NotReady
   until init (the readiness probe is `bao status`, which exits 2 while sealed),
   then flips Ready the instant init auto-unseals it on the toy key.
2. Init with recovery keys (auto-unseal ⇒ recovery, not unseal, keys), enable the
   transit engine + key + the least-privilege policy:

```sh
baoT operator init -recovery-shares=1 -recovery-threshold=1   # record recovery key + root token (dev: low-value)
baoT status        # ⏹ Sealed: false  (auto-unsealed on the toy key)
baoT login <root-token>
baoT secrets enable transit
baoT write -f transit/keys/autounseal
baoT policy write autounseal-use - <<'HCL'
path "transit/encrypt/autounseal" { capabilities = ["update"] }
path "transit/decrypt/autounseal" { capabilities = ["update"] }
HCL
```

The seal token itself — an **orphan + periodic** token on this policy — is minted
in Stage 2.

⏹ **Negative test (#1 trust scoping):** a token on `autounseal-use` can ONLY
wrap/unwrap the one key. The transit Bao has no kv engine by design, so seed one
first or the denial is a false "missing-mount" pass instead of a real 403:

```sh
baoT secrets enable -path=kv kv-v2 && echo dGVzdA== | baoT kv put kv/probe v=-
# Mint via `token create` (flag form). NB: `bao write auth/token/create -policy=` is a
# FOOTGUN — on `write`, -policy is parsed as a data field and ignored, yielding a ROOT
# token that makes this test falsely pass. Always use `token create -policy=`.
TOK=$(baoT token create -policy=autounseal-use -format=json | jq -r .auth.client_token)
kubectl -n openbao exec -i openbao-transit-0 -- env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$TOK" bao kv get kv/probe        # ⏹ MUST fail: 403 permission denied
kubectl -n openbao exec -i openbao-transit-0 -- env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$TOK" sh -c 'echo dGVzdA== | bao write transit/encrypt/autounseal plaintext=-'  # ⏹ succeeds
baoT secrets disable kv    # leave the transit Bao seal-only
```

---

## Stage 2 — Main Bao: transit auto-unseal + TLS + HA `retry_join` (irons #4, #6)

Goal: the production listener config — TLS on, `seal "transit"`, three raft peers
that auto-unseal on join.

1. cert-manager serving cert. The self-signed ClusterIssuer is named
   `selfsigned-cluster-issuer` (NOT `selfsigned`; list `clusterissuers` to
   confirm — the others are letsencrypt-prod/stage). The cert Secret must carry
   `ca.crt` (peers need it to trust each other — see retry_join below):

```sh
kubectl apply -f - <<'YAML'
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: openbao-tls, namespace: openbao}
spec:
  secretName: openbao-tls
  issuerRef: {name: selfsigned-cluster-issuer, kind: ClusterIssuer}
  dnsNames:
    - openbao
    - openbao.openbao.svc
    - openbao-active
    - openbao-0.openbao-internal
    - openbao-1.openbao-internal
    - openbao-2.openbao-internal
    - localhost
  ipAddresses: ["127.0.0.1"]
YAML
```

2. Main Bao HelmRelease `server.ha.raft.config` (the prod overlay — NOT the dev
   `tls_disable` block). **Required chart values that the listener depends on:**
   - `global.tlsDisable: false` — the chart derives the in-pod `BAO_ADDR` scheme
     AND the readiness probe (`bao status -tls-skip-verify`) target from this.
     Left at its `true` default with a TLS listener, every probe hits http on the
     https port and pods never go Ready.
   - `server.affinity: ""` — the chart default is a `podAntiAffinity` requiring
     distinct hostnames, so on one node openbao-1/2 stay `Pending` forever.
     Colocating is acceptable under the single-node fidelity caveat.
   - mount `openbao-tls` at `/openbao/tls` (`server.volumes` + `server.volumeMounts`).

```hcl
ui = true
listener "tcp" {
  address       = "[::]:8200"
  cluster_address = "[::]:8201"
  tls_cert_file = "/openbao/tls/tls.crt"
  tls_key_file  = "/openbao/tls/tls.key"
}
storage "raft" {
  path = "/openbao/data"
  # leader_ca_cert_file is REQUIRED: without it a joining peer cannot trust the
  # leader's self-signed serving cert and loops forever on x509 unknown-authority
  # (tls_skip_verify in the stanza does NOT suppress it).
  retry_join { leader_api_addr = "https://openbao-0.openbao-internal:8200" leader_ca_cert_file = "/openbao/tls/ca.crt" }
  retry_join { leader_api_addr = "https://openbao-1.openbao-internal:8200" leader_ca_cert_file = "/openbao/tls/ca.crt" }
  retry_join { leader_api_addr = "https://openbao-2.openbao-internal:8200" leader_ca_cert_file = "/openbao/tls/ca.crt" }
}
seal "transit" {
  address    = "http://openbao-transit.openbao.svc:8200"
  key_name   = "autounseal"
  mount_path = "transit/"
  # token supplied out-of-band as VAULT_TOKEN (extraSecretEnvironmentVars) — see below
}
service_registration "kubernetes" {}
```

Set `server.ha.replicas: 3`. The seal token must be an **orphan + periodic** token
(the documented standard) so the transit seal auto-renews it forever — never a
default-TTL login token. Mint it once on the transit Bao and inject via
`extraSecretEnvironmentVars` → `VAULT_TOKEN` (kept out of the manifest):

```sh
SEALTOK=$(baoT token create -orphan -period=72h -policy=autounseal-use -format=json | jq -r .auth.client_token)
kubectl -n openbao create secret generic openbao-seal-token --from-literal=token="$SEALTOK"
# verify periodic: baoT token lookup -format=json "$SEALTOK" | jq '.data|{period,explicit_max_ttl,renewable,orphan}'
# expect: period=259200, explicit_max_ttl=0, renewable=true, orphan=true
```

(If a pod can be down longer than the period, use the OpenBao Agent auto-auth
sidecar instead and set `disable_renewal=true` — also standard, more moving parts.)

3. Init the main Bao (recovery keys), record the root token as `BAO_ROOT`, then
   verify quorum + auto-unseal. StatefulSet pods come up one at a time; openbao-0
   is NotReady until init, which gates creation of openbao-1/2.

```sh
bao operator init -recovery-shares=1 -recovery-threshold=1   # record root + recovery key; this lineage survives restore
export BAO_ROOT=<root token>
for p in 0 1 2; do kubectl -n openbao exec openbao-$p -- env BAO_ADDR=https://127.0.0.1:8200 BAO_SKIP_VERIFY=true bao status; done
baoR operator raft list-peers
```

⏹ **Expect (#6):** three peers, one leader; every pod `Sealed: false` with no
human (openbao-2 may join as non-voter for a few seconds then auto-promote).
⏹ **Expect (#4):** TLS served — `bao status` over `https://` ok; plain `http://`
to :8200 → `400 Bad Request` / "Client sent an HTTP request to an HTTPS server".
⏹ **Auto-unseal-on-join:** `kubectl delete pod openbao-2` → it rejoins and
returns `Sealed: false` unattended.

---

## Stage 3 — Idempotency (irons #1)

Goal: converging twice, or restarting, never re-initialises or regenerates key
material over live data.

```sh
baoR secrets enable -path=kv kv-v2; baoR kv put kv/canary v=tracer-$(date +%s)
CANARY=$(baoR kv get -field=v kv/canary)
# 1) re-apply (no-op), 2) controlled restart, 3) attempt a second init
flux reconcile helmrelease openbao -n openbao
# The chart sets the STS updateStrategy to OnDelete, so `rollout restart` only
# bumps the template and `rollout status` errors — pods are NOT auto-replaced.
# Do a controlled raft bounce: standbys first, leader LAST, waiting for Ready +
# Sealed:false + 3-voter quorum between each delete. (Verify rollout completeness
# by per-pod controller-revision-hash == sts status.updateRevision, NOT currentRevision.)
kubectl -n openbao delete pod <standby-a>; kubectl -n openbao wait --for=condition=Ready pod/<standby-a> --timeout=180s
kubectl -n openbao delete pod <standby-b>; kubectl -n openbao wait --for=condition=Ready pod/<standby-b> --timeout=180s
kubectl -n openbao delete pod <leader>;    kubectl -n openbao wait --for=condition=Ready pod/<leader>    --timeout=180s
bao operator init -recovery-shares=1 -recovery-threshold=1   # ⏹ MUST fail: "Vault is already initialized"
baoR kv get -field=v kv/canary    # ⏹ equals $CANARY (data + keyring intact)
baoR operator raft list-peers     # ⏹ same cluster, still unsealed
```

⏹ **Guard to encode in `guardian up`:** init only when
`bao status -format=json | jq -e '.initialized==false'`; never recreate/rotate the
transit `autounseal` key or the static toy seal key if the raft path already
exists (regenerating either orphans the keyring and bricks auto-unseal of the
lineage that snapshots restore to); persist init output to a Secret on first init
and read it back on reconverge. (No Bao init code exists under `src/` yet — this
is a forward-looking spec for whoever wires Bao bootstrap into the converge path.)

---

## Stage 4 — One real dev-scoped secret + ESO projection (irons #3)

Goal: a single externally-issued SaaS token (least-priv Cloudflare, scoped to ONE
zone) projected end-to-end, with negative tests on both the projection role and
the token itself.

ESO is NOT a Cozystack component on this install — install it like openbao (Flux):

```sh
kubectl create namespace external-secrets
kubectl apply -f - <<'YAML'
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata: {name: external-secrets, namespace: external-secrets}
spec: {interval: 1h, url: https://charts.external-secrets.io}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: external-secrets, namespace: external-secrets}
spec:
  interval: 30m
  chart:
    spec:
      chart: external-secrets
      version: "2.6.0"
      sourceRef: {kind: HelmRepository, name: external-secrets, namespace: external-secrets}
  values:
    installCRDs: true
    serviceAccount: {name: external-secrets}
YAML
```

Configure OpenBao k8s auth + a path-scoped policy/role, and write the secret. NB:
on OpenBao 2.5.4 the k8s-auth role write **silently ignores `policy=`** — use
`token_policies=` or the role mints zero-policy tokens (ESO then 403s on the read):

```sh
baoR auth enable kubernetes
baoR write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc disable_local_ca_jwt=false
baoR policy write eso-read-cf - <<'HCL'
path "kv/data/external/cloudflare" { capabilities = ["read"] }
HCL
baoR write auth/kubernetes/role/eso \
  bound_service_account_names=external-secrets \
  bound_service_account_namespaces=external-secrets \
  token_policies=eso-read-cf token_ttl=15m
baoR kv put kv/external/cloudflare token="<CF token scoped to ONE zone, DNS:edit only>"
```

Apply a `ClusterSecretStore` (Kubernetes auth → main Bao over https, `caProvider`
= a Secret holding the `ca.crt` from `openbao-tls`) and an `ExternalSecret`
projecting `kv/external/cloudflare` into a `Secret` in namespace `tracer`,
consumed by a pod via env (give the consumer a hardened `securityContext` — the
namespace warns on restricted PodSecurity).

⏹ **Expect:** `kubectl -n tracer get secret cf-token` populated; pod sees it;
sha256(Bao-stored) == sha256(projected).
⏹ **Negative (#3 path scope):** exercise the REAL role
(`auth/kubernetes/login role=eso jwt=<SA JWT>`, not a hand-made token) → reads
`kv/external/cloudflare` OK; reads `kv/canary`, `kv/external/anything-else`,
`sys/auth` → permission denied.
⏹ **Negative (#3 token scope):** with the projected token, DNS API on its zone →
200; any OTHER zone → 403 (Cloudflare 10000 auth error); `/zones` enumerates only
the one zone. A leaked dev token must be near-worthless.

---

## Stage 5 — Scoped snapshot → age → R2 (irons #5)

Goal: the backup path uses a **snapshot-only** identity and reuses the proven
age+R2 mechanism. Two separate scoped credentials: the OpenBao snapshot token AND
the bucket-scoped R2 token (never root, never the operator's TTL'd token).

```sh
kubectl -n openbao create serviceaccount openbao-backup
baoR policy write snapshot-only - <<'HCL'
path "sys/storage/raft/snapshot" { capabilities = ["read"] }
HCL
baoR write auth/kubernetes/role/snapshotter \
  bound_service_account_names=openbao-backup \
  bound_service_account_namespaces=openbao \
  token_policies=snapshot-only token_ttl=10m
```

⏹ **Negative test (#5)** — mint via `token create` (NOT `write … -policy=`, the
root-leak footgun):
```sh
SNAPTOK=$(baoR token create -policy=snapshot-only -format=json | jq -r .auth.client_token)
EXEC="kubectl -n openbao exec -i openbao-0 -- env BAO_ADDR=https://127.0.0.1:8200 BAO_SKIP_VERIFY=true BAO_TOKEN=$SNAPTOK bao"
$EXEC kv get kv/canary                     # ⏹ MUST fail: 403 permission denied
$EXEC read sys/auth                        # ⏹ MUST fail: 403
$EXEC operator raft snapshot save /tmp/t.snap   # ⏹ succeeds
```

Then the CronJob shape (manual run here): snapshot → age-encrypt to the
survival-floor recipient → upload to `guardian-vault` under `openbao/`:

```sh
kubectl -n openbao exec openbao-0 -- env BAO_ADDR=https://127.0.0.1:8200 BAO_SKIP_VERIFY=true BAO_TOKEN="$SNAPTOK" \
  bao operator raft snapshot save /tmp/snap.snap
kubectl -n openbao cp openbao-0:/tmp/snap.snap /tmp/openbao-$(date +%F).snap
age -r "$age_recipient" -o /tmp/openbao-$(date +%F).snap.age /tmp/openbao-$(date +%F).snap
# upload with the bucket-scoped R2 trio (boto3 snippet from survival-floor.md), key openbao/openbao-<date>.snap.age
```

⏹ **Expect:** one `uploaded openbao/...age` line; the age identity is NOT on the
cluster and NOT in R2 — only ciphertext leaves. (Requires `age_recipient` in
`secret.env` — see the preamble; absent on the first run, so the upload leg is the
one open item.)

---

## Stage 6 — DR: restore → auto-unseal → verify (irons #1, #2 closed loop)

Goal: prove recovery of the **one secret** end to end — keyring lineage and the
`Retain` zvol survival — without endangering the live cluster. **Safe form: no
node wipe, no live-PVC delete.**

1. **#2 Retain proof (throwaway, net-zero):** bind a scratch PVC on
   `openbao-retain`, confirm the backing volume exists, delete the PVC, and
   confirm the volume is RETAINED:

```sh
# create a throwaway PVC + tiny pod to trigger WaitForFirstConsumer binding, then:
kubectl -n openbao delete pvc retain-proof
kubectl get pv | grep retain-proof   # ⏹ PV phase=Released (NOT gone)
# zvol still present — verify via linstor (node is SSH-less, no `zfs list`):
kubectl -n cozy-linstor exec deploy/linstor-controller -c linstor-controller -- linstor resource list | grep <pv>  # ⏹ still listed
```

⏹ **#2:** Retain does NOT cascade-delete. Even deleting the Released PV leaves the
LINSTOR resource orphaned — manual GC is required:
`linstor resource-definition delete <pv>`.

2. **Restore proof:** restore a fresh snapshot into a **scratch** Bao, leaving the
   live main Bao untouched. The scratch MUST use the **same transit seal** as the
   source (a raft snapshot carries the source's encrypted keyring; a different
   seal — e.g. a static toy key — fails with "failed to decrypt encrypted stored
   keys"). The transit Bao must be up first.

```sh
baoR operator raft snapshot save /tmp/restore.snap                 # fresh snapshot of live main Bao
kubectl -n openbao cp openbao-0:/tmp/restore.snap /tmp/restore.snap
# helm install openbao-scratch (replicas:1, openbao-retain, SAME transit seal as source,
#   its own freshly-minted autounseal-use seal token); then:
SCRATCH="kubectl -n openbao exec -i openbao-scratch-0 -- env BAO_ADDR=https://127.0.0.1:8200 BAO_SKIP_VERIFY=true bao"
$SCRATCH operator init -recovery-shares=1 -recovery-threshold=1    # transient bare cluster (keys discarded by restore)
kubectl -n openbao cp /tmp/restore.snap openbao-scratch-0:/tmp/restore.snap
$SCRATCH operator raft snapshot restore /tmp/restore.snap
$SCRATCH status                                                    # ⏹ Sealed: false (auto-unseals via transit on restored keyring)
# with the SOURCE root token: kv/canary == the live value (lineage came from the snapshot)
```

⏹ **#1 lineage:** after restore the scratch reverts to the **source** Cluster ID
and recovery keys; the transient bare-cluster root token stops working. The canary
matches. Tear the scratch down afterward (helm uninstall + PVC + Released PV +
orphaned LINSTOR resource + seal-token secret).

3. **Seal-migration sub-drill (OPTIONAL — not run in the first drill).** Migrating
   seals (e.g. static↔transit) is the prod op most likely to brick a vault. To
   drill it: add `disabled = "true"` to the OLD seal stanza alongside the NEW one,
   restart, then `bao operator unseal -migrate <recovery-key>` (auto→auto
   migration uses recovery keys). Document the exact steps that worked.

---

## Teardown — settle dev to the daily-light config

The tracer's prod overlay (transit Bao, 3 replicas, TLS) is heavier than daily
dev. After recording results, either revert dev to single main Bao + static toy
seal + `replicas: 1` + `tls_disable=1` (the daily-light `openbao.yaml`), or
promote this overlay. Keep the proven **prod overlay** (transit + TLS +
retry_join + periodic seal token) checked in so prod inherits a drilled config,
not a fresh guess.

## Record

First run: 2026-06-19, `guardian-nonprod` / `ash-bm-004` (k8s v1.36.1, Talos
v1.13.0). All stages passed; #5 partial (off-cluster upload leg unproven).

| date | stage | result | notes / gotchas |
|---|---|---|---|
| 2026-06-19 | 0 Retain SC | PASS | clean create; `data` pool live, Retain confirmed |
| 2026-06-19 | 1 transit + neg test | PASS | neg test real only after seeding `kv/probe`; token via `token create` |
| 2026-06-19 | 2 TLS/HA/retry_join | PASS | needed `leader_ca_cert_file`, `global.tlsDisable:false`, `affinity:""` |
| 2026-06-19 | 3 idempotency | PASS | 2nd `init` rejected; OnDelete controlled bounce; canary intact |
| 2026-06-19 | 4 ESO + neg tests | PASS | ESO installed via HelmRelease; `token_policies=` fix; real single-zone CF token |
| 2026-06-19 | 5 scoped snapshot | PARTIAL | snapshot + neg tests PASS; age+R2 upload **not run** (no `age_recipient`) |
| 2026-06-19 | 6 DR restore | PASS | scratch w/ SAME transit seal; Retain proven; live untouched; migration skipped |

### Corrections folded in from the first run

The initial draft of this runbook had these bugs (all fixed above):

- **`retry_join` lacked `leader_ca_cert_file`** → peers never join over self-signed TLS (CRITICAL).
- **`global.tlsDisable: false` omitted** → readiness probe hits http on the https port; pods never Ready.
- **Default `podAntiAffinity`** → extra replicas Pending on one node; needs `server.affinity: ""`.
- **Wrong issuer name** `selfsigned` → it is `selfsigned-cluster-issuer`.
- **`policy=` silently ignored** on k8s-auth role writes (2.5.4) → use `token_policies=` (hit roles in Stages 1/4/5).
- **`bao write auth/token/create -policy=` leaks a ROOT token** → negative tests falsely pass; use `bao token create -policy=`.
- **Injected `BAO_TOKEN` (seal token) overrides `bao login`** → admin calls need `BAO_TOKEN=<root>` (the `baoR` helper).
- **OnDelete update strategy** → `rollout restart`/`status` don't work; do a controlled per-pod bounce.
- **Stage 6 scratch must use the SAME transit seal**, not a static toy seal, or restore fails to decrypt the keyring.
- **ESO is not a Cozystack component** here → install via HelmRelease (chart `external-secrets` 2.6.0).
- **Storage is LINSTOR/DRBD on ZFS-thin** → inspect volumes via `linstor`, not `zfs list` (SSH-less node).
