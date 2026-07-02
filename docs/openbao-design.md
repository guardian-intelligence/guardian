# OpenBao secrets platform — design (target state)

Status: **IMPLEMENTATION IN PROGRESS.** The repo now declares static seal,
self-init, independent cert-manager listener TLS, `openbao-local` retained
storage, KV, and Transit. The old manual-unseal/operator-bootstrap path and
the OpenBao-issued listener certificate path have been removed from the happy
path. Remaining target-state gaps called out below include the exact
`zfsThinPool` substrate, hostPath admission enforcement in the live cluster,
and tested stateful restore.

Scope: the Guardian tenant OpenBao (3-node raft) secrets platform. OpenBao is
one bootstrapping component of the system, not the system's end state; the
platform-wide convergence proof is `aspect infra converged`, and manifest
conformance testing is designed in `docs/manifest-conformance-design.md`.
Decisions below were reached deliberately; the load-bearing trade-offs are
called out explicitly.

---

## Topology & storage
- 3-node raft `StatefulSet` in `tenant-guardian`, one member per node, hard pod + node
  anti-affinity so a single node loss removes exactly one member (quorum 2/3).
- **Local storage per member** — node-pinned `zfsThinPool` (replica=1) StorageClass, one
  PVC per pod, for both the raft data volume and the audit volume. **Not `replicated`
  (LINSTOR).** Raft already replicates at the application layer; replicated block storage
  underneath multiplies physical copies, adds a network hop to the latency-critical
  per-entry `fsync`, and introduces double-attach/fencing risk a consensus store exists to
  avoid. Local storage is HashiCorp's documented model for integrated storage.
- The three members are pinned to three **dedicated, tainted, key-bearing nodes**: local PV
  + seal-key placement co-locate on the same three nodes. General workloads are kept off via
  taints; admission blocks any other hostPath/privileged pod from mounting the key directory.
- `updateStrategyType: OnDelete`; Flux `upgrade.disableWait: true`, `remediation.retries: 0`
  — Flux never thrashes quorum; pods roll one at a time. (Trade-off: this can mask a
  non-converging release; the drills are the compensating convergence check.)

## Seal — static auto-unseal
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
  self-init. The OpenBao status drill (`aspect infra openbao-drill`) rejects
  mixed OpenBao `cluster_id` values across raft members.

## Static seal security posture
- The static seal key is a node-local bearer secret. It is placed out of band on the three
  dedicated OpenBao nodes and is never stored in Git, Kubernetes, CI, Talos `machine.files`,
  chat, shell history, or OpenBao-backed secret paths.
- **Node/root compromise is OpenBao compromise.** Anyone who can read the static seal key
  and OpenBao raft data from a key-bearing node can recover the cluster. Static file seal
  is accepted for unattended restart, simple DR, and avoiding a second control plane; it is
  not treated as a hardware-backed trust boundary.
- A freshly rebuilt node intentionally has no key until an operator re-places it from
  backup custody. That is part of the DR posture, not a convergence failure.

## Listener TLS
- cert-manager issues **one** cert for the **8200 API listener**:
  `Certificate/guardian-openbao-api` writes `Secret/guardian-openbao-api-tls`.
- The OpenBao listener certificate is transport identity only. It is issued by
  `Issuer/guardian-openbao-listener-ca`, backed by
  `Certificate/guardian-openbao-listener-ca`, and does not depend on OpenBao.
  A wiped OpenBao cluster can start as soon as cert-manager and the static seal
  file are present.
- This accepts the narrower Kubernetes/cert-manager risk: a compromised cluster
  could mint listener transport identity, but it does not reveal the seal key,
  root/recovery material, Transit keys, KV contents, or OpenBao raft data.
- The Talos cluster CA remains rejected as issuer; using it requires
  exfiltrating the control-plane root key into a tenant workload.
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
  Reloader is now *tolerable* since static-seal makes restarts self-healing. The sidecar
  matches the running `bao` server process before sending SIGHUP.
- **Rolling order** under `OnDelete`: standbys first, one at a time → `operator step-down` →
  former-active last. Never `kubectl rollout restart`.
- **CA rotation** (the hard part; leaf rotation is trivial) uses a trust-overlap window via
  **trust-manager** once there are external OpenBao clients that need an explicit
  public bundle: publish an old+new CA bundle, wait for clients to trust both,
  switch the issuer, re-issue leaves node-by-node, retire the old CA last.
  cert-manager's CA issuer does not check whether a leaf outlives the CA; track
  CA expiry independently.
- **Later option (not now):** OpenBao ≥2.2 native ACME auto-TLS for the 8200 listener could
  retire the mount+SIGHUP pipeline, but needs an external ACME server (e.g. step-ca) as a new
  dependency. Deferred; cert-manager + SIGHUP sidecar is the lower-dependency path.

## Workload PKI
- OpenBao PKI is not used for OpenBao's own listener certificate.
- No workload PKI consumer exists yet, so the repo does not carry a PKI mount,
  root issuer, role, cert-manager Vault issuer, or cert-manager TokenRequest
  plumbing.
- When a real workload PKI consumer appears, reintroduce it as workload PKI:
  mount `pki/workload`, role `guardian-workload`, cert-manager issuer
  `guardian-openbao-workload`, and policy limited to the approved
  `pki/workload/sign/<role>` path.
- The target custody model for workload PKI is an offline-held root CA outside
  Kubernetes/OpenBao and an OpenBao-held intermediate. cert-manager requests
  workload leaves through the Vault issuer; workloads trust the offline root
  distributed by trust-manager. Do not generate a workload PKI root inside
  OpenBao unless that is explicitly accepted as the custody boundary.

## Transit
- `OpenBaoMount/transit` declares the Transit engine so approved key-management
  consumers can be added without reintroducing a bootstrap control plane.
- Durable Transit keys are not declared generically. A Transit key that protects
  durable ciphertext is data-loss-critical. **Decided custody model:** create
  such keys with `exportable=true` and `allow_plaintext_backup=true`, and export
  the keyring with `transit/backup` into offline custody at creation time.
  The export is the full plaintext keyring — holding it means decrypting all
  ciphertext under that key — so it lives in the same offline custody tier as
  the static seal key, never in R2, and never co-located with the ciphertext it
  protects. Exportability does not weaken the boundary: the seal key on node
  disk is already the root of trust, and the export extends the same custody
  discipline to one more artifact.
- Non-durable drill keys may be created imperatively during DR verification and
  deleted afterward.

## Auth & self-init config
- Kubernetes auth method; ESO and the ops-controller authenticate via SA tokens validated by
  TokenReview, bound to namespace + SA + audience, least-privilege policies.
- Self-init creates the ops-controller policy + auth role required for steady state, plus a
  temporary `guardian-secret-importer` role used only to move local break-glass credential files
  into OpenBao. The importer is a one-time, bootstrap-only, heavily cordoned, non-load-bearing
  task; it must be sunset once the initial local credentials are inside OpenBao. The importer
  deletes its own role and policy after a successful import. OpenBao config then converges via
  Flux-applied CRs + the OpenBao ops controller. There is no operator-held privileged token in
  the steady-state path.
- ESO is the consumer: SecretStore/ClusterSecretStore → OpenBao KV; ExternalSecrets
  materialize native Secrets. OpenBao is source of truth; the k8s Secret is a synced cache.

## Audit
- A **single local `file` audit device** (the device OpenBao blocks on if audit writes fail).
  Rotate by **rename + SIGHUP** (not copytruncate); alert before disk pressure can block bao.
- **No network audit "fallback" device.** A second socket/syslog device *enlarges* the
  blocking surface (a network audit device is more block-prone), so it makes availability
  worse, not better.
- A sidecar tails the file and ships rotated logs to **Cloudflare R2 with a bucket-lock rule
  (WORM)** for tamper-evident offsite retention — strictly async/downstream, never in the
  request path. R2 is not an OpenBao audit device.

## Disaster recovery
- Primary cold-start model: rebuild from Git, supply custody-held residue, and
  verify the system converges. The residue inventory is checked in at
  `docs/openbao-residue-inventory.md`.
- Stateful restore model: the primary recovery for durable Transit keys is
  `transit/restore` from the keyring export taken at key creation. A raft
  snapshot is the optional second path for non-derivable OpenBao state; it is
  barrier-encrypted (unreadable without the static seal key, so offsite storage
  is acceptable) but still sensitive custody material, not an ordinary backup
  artifact.
- **No recovery keys.** Self-init emits no root token and no recovery keys, and
  none are established afterward. Recovery keys cannot decrypt the barrier;
  they only authorize `generate-root`. Break-glass admin access is regenerate:
  wipe, cold-start from custody residue, `transit/restore` durable keys.
- **Snapshot/restore automation is deferred pending Velero research.** Do not add a
  second backup control plane or a bespoke snapshot CronJob until we decide whether Velero
  can carry the OpenBao raft/PVC and restore-drill requirements cleanly. Until then, keep
  manual `bao operator raft snapshot` recovery as the known primitive, and keep it out of
  day-to-day convergence.
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
  3-node cluster with the same static key, `snapshot restore -force`, unseal. Transit
  keyrings and KV state return intact because they live inside the barrier the
  snapshot carries.
- **Tested restores:** required before relying on OpenBao Transit for durable
  ciphertext. The drill must cold-start a throwaway cluster, `transit/restore`
  the exported keyring, and assert a Transit decrypt of pre-restore ciphertext;
  when exercising the snapshot path, the throwaway cluster must start with the
  same static key and pass the same decrypt check after snapshot restore.

## Seal-key rotation
- `previous_key`/`current_key` under a new key-id; roll one pod at a time; keep the previous
  key until rewrap/seal-migration evidence clears it **and** no retained snapshot still needs
  it. Key contents never change without the key-id changing.

---

## Bootstrap ordering (target sequence)

Talos up → break-glass place seal key on the 3 pinned nodes → **cert-manager up** →
independent cert-manager listener CA creates `guardian-openbao-api-tls` →
**OpenBao up** (static-seal auto-unseals; consumes the listener cert from birth) →
self-init creates ops-controller/auth access → OpenBao ops enables KV, Transit,
policies, and auth roles → ESO consumes. Flux ordering must be real
(cert-manager Ready → listener CA Certificate Ready → listener leaf Certificate Ready →
OpenBao pods → OpenBao ops Ready); `disableWait` makes this easy to race, so encode
the dependency explicitly.

---

## Remaining Ops Inventory

Already declared and Ready in the current cluster:
- `kv` mount and tune.
- `transit` mount.
- Kubernetes auth backend.
- Ops-controller policy and Kubernetes auth role.
- `external-dns` read policy and Kubernetes auth role.
- `ClusterSecretStore/external-dns-openbao` and `ExternalSecret/cloudflare-external-dns`.

Legacy live cruft removed on 2026-06-29:
- Deleted `tenant-root/ExternalSecret guardian-cnpg-backup-creds`,
  `tenant-root/ExternalSecret guardian-clickhouse-backup-creds`,
  `tenant-root/SecretStore openbao`, and `tenant-root/SecretStore openbao-clickhouse-backup`.
  They pointed at retired `http://openbao-guardian.tenant-root.svc:8200`. Cozystack 1.5
  system bucket/backups should remain platform-managed unless we intentionally reintroduce
  an OpenBao-backed credential projection.

Ops resources still needed before OpenBao is the real vault/transit authority:
- Restore drill for any durable Transit key before that key protects production
  ciphertext.
- Declarative Transit-key resources only when a real consumer requires them. The
  current operator can declare mounts/policies/auth roles, but it has no
  `OpenBaoTransitKey` CRD/controller yet.
- KV policies/auth roles/ExternalSecrets for any remaining consumers of imported
  Cloudflare/R2 credentials beyond `external-dns`.
- Release/signing, artifact provenance, envelope encryption, or workload key-management
  components should use Transit rather than Kubernetes Secrets as the key authority.
  DNS does not need Transit; it only needs a Cloudflare API token from KV.

## Key references
- Integrated storage = local filesystem + consensus; remove-peer / peers.json recovery:
  https://developer.hashicorp.com/vault/docs/concepts/integrated-storage
- Raft reference architecture (SSD-optimized, IOPS/latency targets):
  https://developer.hashicorp.com/vault/tutorials/day-one-raft/raft-reference-architecture
- OpenBao raft backend: https://openbao.org/docs/configuration/storage/raft/
- OpenBao static seal: https://openbao.org/docs/configuration/seal/static/
- OpenBao seal/unseal concepts: https://openbao.org/docs/concepts/seal/
- OpenBao Kubernetes auth: https://openbao.org/docs/auth/kubernetes/
- OpenBao Transit secrets engine: https://openbao.org/docs/secrets/transit/
- OpenBao PKI considerations for future workload PKI: https://openbao.org/docs/secrets/pki/considerations/
- cert-manager CA issuer: https://cert-manager.io/docs/configuration/ca/
- cert-manager Vault issuer for future workload PKI: https://cert-manager.io/docs/configuration/vault/
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
