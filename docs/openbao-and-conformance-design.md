# OpenBao secrets platform & manifest conformance — design (target state)

Status: **IMPLEMENTATION IN PROGRESS.** The repo now declares static seal,
self-init, native OpenBao TLS, `openbao-local` retained storage, and Tier 1
semantic conformance checks. The old manual-unseal/operator-bootstrap path has been
removed from the happy path. Remaining target-state gaps called out below
include the exact `zfsThinPool` substrate, TPM-sealed node storage, and the
final CA distribution/rotation shape.

Scope: the Guardian tenant OpenBao (3-node raft) secrets platform, and Tier 1 manifest
conformance testing. Decisions below were reached deliberately; the load-bearing
trade-offs and the two genuinely-open questions are called out at the end.

---

## Part 1 — OpenBao secrets platform

### Topology & storage
- 3-node raft `StatefulSet` in `tenant-guardian`, one member per node, hard pod + node
  anti-affinity so a single node loss removes exactly one member (quorum 2/3).
- **Local storage per member** — node-pinned `zfsThinPool` (replica=1) StorageClass, one
  PVC per pod, for both the raft data volume and the audit volume. **Not `replicated`
  (LINSTOR).** Raft already replicates at the application layer; replicated block storage
  underneath multiplies physical copies, adds a network hop to the latency-critical
  per-entry `fsync`, and introduces double-attach/fencing risk a consensus store exists to
  avoid. Local storage is HashiCorp's documented model for integrated storage.
- The three members are pinned to three **dedicated, tainted, key-bearing nodes**: local PV
  + seal-key placement + (if the TPM tracer is GO) TPM-sealed disk all co-locate on the same
  three nodes. General workloads are kept off via taints; admission blocks any other
  hostPath/privileged pod from mounting the key directory.
- `updateStrategyType: OnDelete`; Flux `upgrade.disableWait: true`, `remediation.retries: 0`
  — Flux never thrashes quorum; pods roll one at a time. (Trade-off: this can mask a
  non-converging release; the drills are the compensating convergence check.)

### Seal — static auto-unseal
- `seal "static"` with `current_key = file:///openbao/secrets/unseal-<id>.key`, a 32-byte
  raw AES-256-GCM key, read-only mounted from node storage.
- An init container hard-fails the pod unless the key file exists **and its fingerprint
  matches `current_key_id`** (not merely "is 32 bytes").
- Any restart self-unseals from the file. "Sealed after init" is a **fault**, not a resting
  state, and health/readiness reflect that.
- OpenBao self-init performs the cluster-level initialize operation once. With
  `podManagementPolicy: OrderedReady`, `guardian-openbao-0` is the first eligible member. Later
  members retry-join through `guardian-openbao-active`; they must not list their own ordinal as a
  retry target, because a first-start follower can otherwise fall back to an independent
  self-init. The cutover proof rejects mixed OpenBao `cluster_id` values across raft members.

### Trust root for the seal key — TPM (tracer-gated)
- The key exists only on **TPM-sealed Talos node storage** (luks2 + `tpm` key provider)
  under Secure Boot. Placed via break-glass node-config (never `machine.files`, which would
  leak it into config). A freshly rebuilt node has no key until an operator re-places it
  (accepted; matches the DR posture).
- Honest scope: TPM protects the key **at rest** (stolen disk / RMA / decommission), **not**
  a live-node compromise (the file is plaintext while mounted).
- **GO-gated on a tracer-bullet** on one Latitude node (see Open Decisions). NO-GO fallback:
  transit-bao, or `file://` on unencrypted disk with a runbook that *admits* there is no
  at-rest protection. `nodeID` and cloud-KMS are rejected.

### Native listener TLS
- cert-manager issues **one** cert — for the **8200 API listener** — from a Guardian
  internal CA (the Talos cluster CA is rejected as issuer; using it requires exfiltrating the
  control-plane root key into a tenant workload).
- **The 8201 raft cluster/peer cert is self-managed by OpenBao** (its own internal CA,
  rotated automatically). cert-manager does not touch 8201. `retry_join` uses the 8200 cert
  only during first join (`leader_tls_servername` matching a SAN, `leader_ca_cert_file` trusting
  the internal CA); after join, peer traffic is on the self-managed 8201 cert.
- **One shared multi-SAN Certificate** mounted on all three pods (not per-pod). SANs cover
  per-pod headless DNS (`guardian-openbao-N.guardian-openbao-internal` + `.svc...`), both
  services, the `leader_tls_servername` value, and localhost. Mount **without `subPath`**
  (subPath mounts never receive Secret updates).
- **Reload:** a **SIGHUP sidecar** watching the mounted cert file (OpenBao reloads cert
  *content* at the same path on SIGHUP; there is no native file-watcher). Preferred over
  Stakater Reloader because it avoids restarts and any quorum risk — though restart-based
  Reloader is now *tolerable* since static-seal makes restarts self-healing.
- **Rolling order** under `OnDelete`: standbys first, one at a time → `operator step-down` →
  former-active last. Never `kubectl rollout restart`.
- **CA rotation** (the hard part; leaf rotation is trivial) uses a trust-overlap window via
  **trust-manager**: publish an old+new CA bundle, wait for all nodes to trust both, switch
  the issuer, re-issue leaves node-by-node, retire the old CA last. cert-manager's CA issuer
  does not check whether a leaf outlives the CA — track CA expiry independently.
- **Later option (not now):** OpenBao ≥2.2 native ACME auto-TLS for the 8200 listener could
  retire the mount+SIGHUP pipeline, but needs an external ACME server (e.g. step-ca) as a new
  dependency. Deferred; cert-manager + SIGHUP sidecar is the lower-dependency path.

### Auth & self-init config
- Kubernetes auth method; ESO and the ops-controller authenticate via SA tokens validated by
  TokenReview, bound to namespace + SA + audience, least-privilege policies.
- Self-init creates the ops-controller policy + auth role required for steady state, plus a
  temporary `guardian-secret-importer` role used only to move local break-glass credential files
  into OpenBao. The importer deletes its own role and policy after a successful import. OpenBao
  config then converges via Flux-applied CRs + the OpenBao ops controller. There is no
  operator-held privileged token in the steady-state path.
- ESO is the consumer: SecretStore/ClusterSecretStore → OpenBao KV; ExternalSecrets
  materialize native Secrets. OpenBao is source of truth; the k8s Secret is a synced cache.

### Audit
- A **single local `file` audit device** (the device OpenBao blocks on if audit writes fail).
  Rotate by **rename + SIGHUP** (not copytruncate); alert before disk pressure can block bao.
- **No network audit "fallback" device.** A second socket/syslog device *enlarges* the
  blocking surface (a network audit device is more block-prone), so it makes availability
  worse, not better.
- A sidecar tails the file and ships rotated logs to **Cloudflare R2 with a bucket-lock rule
  (WORM)** for tamper-evident offsite retention — strictly async/downstream, never in the
  request path. R2 is not an OpenBao audit device.

### Disaster recovery — OpenBao is authoritative (decision b)
- OpenBao **is** authoritative (it runs PKI and transit engines). The earlier "no restore /
  rebuild-and-reproject" stance is **retired**. Recovery is **raft snapshots**.
- **No automated snapshotting in OpenBao OSS** (that is Vault Enterprise). A Kubernetes
  **CronJob** (every 1–6h) with a minimal-policy token (`update` on
  `sys/storage/raft/snapshot`) runs `bao operator raft snapshot save`, the snapshot is
  envelope-encrypted (age/sops, key not co-located with R2 creds), and uploaded to R2.
- **WORM on R2 = bucket-lock rule, not S3 Object Lock** (R2 has no per-object retention and
  no write-without-delete token). The uploader gets a **bucket-scoped Object R/W token**; the
  lock rule is created/owned by a **separate admin credential** held out-of-band. The lock,
  not token scope, is what blocks deletion.
- **The 32-byte static seal key is a first-class backup artifact.** A snapshot is
  barrier-encrypted and only restorable with the same seal, so the key must be backed up
  **separately from the snapshots, under different custody**, and **old key versions retained
  for as long as any snapshot they encrypted still exists** (this couples seal-key rotation to
  snapshot retention). Without the key, snapshots are unrecoverable ciphertext.
- **Restore is whole-cluster only** in OSS (no partial/`inspect`/`recover`). DR = init a fresh
  3-node cluster with the same static key, `snapshot restore -force`, unseal. PKI CA keys and
  the transit keyring return intact because they live inside the barrier the snapshot carries.
- **Tested restores:** a periodic automated restore drill into a throwaway cluster (same
  static key) asserting unseal + a PKI issue + a transit encrypt/decrypt round-trip. Alert on
  newest-snapshot age (> ~1.5× the CronJob period) and on CronJob failure.

### Seal-key rotation
- `previous_key`/`current_key` under a new key-id; roll one pod at a time; keep the previous
  key until rewrap/seal-migration evidence clears it **and** no retained snapshot still needs
  it. Key contents never change without the key-id changing.

---

## Part 2 — Manifest conformance (Tier 1)

Principle: validate the **rendered artifact that ships**, against schema and against the real
API server's admission — never source templates, never hand-restated field values. Tier 1
owns three failure classes: structural validity, CRD-schema validity, admission/PSA
admissibility. Policy-as-code (Kyverno) is a later tier. This replaces ~770 brittle
pinned-value Go assertions.

- **Stage A** (offline, hermetic, every PR, in `bazel test //...`): per-overlay render
  (`kustomize build` / `flux build`) → `kubeconform -strict` against vendored core + CRD
  schemas (version-pinned, generated from the exact deployed CRDs + community catalog).
  **Fail on any un-allowlisted skip** (logging is not enough) so "skipped" never reads as
  "passed."
- **Stage B** (online, CI-gated): rendered output →
  `kubectl apply --server-side --dry-run=server --validate=strict`, against a **per-run
  ephemeral cluster seeded from the repo's own declared CRDs + PSA-labeled namespaces +
  ValidatingAdmissionPolicies + webhook configs + ResourceQuotas/LimitRanges + storage
  classes**. Chosen over a standing prod kubeconfig in CI (least dangerous; can't drift
  because it's built from the same manifests prod is). **Capture API warnings as test output;
  fail on unknown-field warnings.**
- **Helm expanded**: HelmRelease-backed components are rendered to expanded manifests via
  `helm install --dry-run=server` (faithful `.Capabilities`/`lookup` from the cluster), then
  pass through kubeconform + dry-run. Only charts where we inject non-trivial values are in
  scope. If a chart uses `lookup`, either seed exactly what prod has or ban `lookup`.
  Acknowledged ~95% fidelity vs Flux's in-cluster HelmController render; the last 5% (Flux
  value-merge/postRenderers/reconcile) is a later tier that runs the actual Flux controllers.
- **Custom Go tests survive only for cross-field semantic invariants** no schema/admission
  check can express (e.g. seal stanza `current_key_id` ↔ init-container filename agreement;
  referenced runbook exists). Per-field value checks become snapshots; "all resources must…"
  rules wait for the policy tier.

---

## Bootstrap ordering (target sequence)

Talos up → offline root signs intermediate (root stays offline) → break-glass place
{intermediate CA key Secret, seal key on the 3 pinned nodes} → **cert-manager up** (CA
issuer; trust-manager publishes the CA bundle) → cert-manager issues the OpenBao 8200 leaf →
**OpenBao up** (static-seal auto-unseals; consumes the cert from birth) → self-init creates
ops-controller/auth access → ESO consumes. cert-manager before OpenBao eliminates any
temporary-cert + revoke step (cert-manager does not depend on OpenBao). Flux ordering must be
real (cert-manager Ready → CA Issuer Ready → Certificate Ready → OpenBao pods); `disableWait`
makes this easy to race, so encode the dependency explicitly.

---

## Open decisions

1. **TPM tracer GO/NO-GO** (one Latitude node). Pass criteria: Secure Boot enforced; TPM 2.0
   present/usable (note discrete vs firmware TPM); PCR policy survives a Talos upgrade and
   planned firmware updates without bricking decrypt; unauthorized boot media/kernel/initrd
   does not unlock; cold power-cycle, staged upgrade, rollback, disk-move, TPM-loss, and
   node-replacement drills pass; recovery when one key node is rebuilt and intentionally lacks
   the key. NO-GO → transit-bao or honest-unencrypted fallback.
2. **TLS CA shape**: a short-lived **intermediate chained under an offline root** (preferred —
   root key never online) vs. a cert-manager self-signed root. Plus whether/when to revisit
   OpenBao native ACME (deferred). Does not change how OpenBao consumes the cert.

---

## Key references
- Integrated storage = local filesystem + consensus; remove-peer / peers.json recovery:
  https://developer.hashicorp.com/vault/docs/concepts/integrated-storage
- Raft reference architecture (SSD-optimized, IOPS/latency targets):
  https://developer.hashicorp.com/vault/tutorials/day-one-raft/raft-reference-architecture
- OpenBao raft backend: https://openbao.org/docs/configuration/storage/raft/
- OpenBao static seal: https://openbao.org/docs/configuration/seal/static/
- OpenBao seal/unseal concepts: https://openbao.org/docs/concepts/seal/
- TCP listener `reloads-on-SIGHUP`: https://openbao.org/docs/configuration/listener/tcp/ ·
  https://developer.hashicorp.com/vault/docs/configuration/listener/tcp
- Replace TLS cert without restart (SIGHUP): https://support.hashicorp.com/hc/en-us/articles/4417759906835
- Safe raft restart order (standby-first, OnDelete): https://support.hashicorp.com/hc/en-us/articles/23744227055635
- HA TLS example (shared multi-SAN cert, 8201 self-managed): https://developer.hashicorp.com/vault/docs/deploy/kubernetes/helm/examples/ha-tls
- Stakater Reloader: https://github.com/stakater/Reloader · trust-manager: https://cert-manager.io/docs/trust/trust-manager/
- OpenBao ACME TLS listeners (≥2.2): https://openbao.org/docs/rfcs/acme-tls-listeners/
- `operator raft snapshot` save/restore: https://openbao.org/docs/commands/operator/raft/
- Vault snapshot restore (`-force`, seal compat): https://developer.hashicorp.com/vault/docs/sysadmin/snapshots/restore
- OpenBao auto-snapshot gap (Enterprise-only in Vault): https://github.com/openbao/openbao/issues/795
- R2 bucket locks (WORM): https://developers.cloudflare.com/r2/buckets/bucket-locks/ · R2 tokens: https://developers.cloudflare.com/r2/api/tokens/
- kubeconform: https://github.com/yannh/kubeconform
