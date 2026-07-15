# OpenBao secrets platform — design (target state)

Status: **IMPLEMENTATION IN PROGRESS.** The repo now declares static seal,
self-init, independent cert-manager listener TLS, `openbao-local` retained
storage, KV, and Transit. The old manual-unseal/operator-bootstrap path, the
OpenBao-issued listener certificate path, and the custom `openbao-ops-controller`
operator (with its CRDs and hand-authored operation CRs) have all been removed:
OpenBao config now lives entirely in the self-init `initialize` block. Remaining
target-state gaps called out below include the exact `zfsThinPool` substrate,
hostPath admission enforcement in the live cluster, and tested stateful restore.

Scope: the Guardian tenant OpenBao (3-node raft) secrets platform. OpenBao is
one bootstrapping component of the system, not the system's end state; the
platform-wide convergence proof is `aspect infra converged`, and manifest
conformance testing is decided in `docs/adrs/0003-validate-rendered-manifests.md`.
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
- Self-init's `enable_transit_mount` request declares the Transit engine so approved
  key-management consumers can be added without reintroducing a bootstrap control plane.
- Durable Transit keys are not declared generically. A Transit key that protects
  durable ciphertext is data-loss-critical: it survives reinit and disaster
  recovery as data, through the barrier-encrypted raft snapshots continuously
  shipped to R2 (`docs/secrets.md` §Disaster recovery). No plaintext keyring
  ever leaves OpenBao — keys are created non-exportable, and the snapshot is
  unreadable without the custody-held seal key, so R2 holding it violates no
  boundary.
- Non-durable drill keys may be created imperatively during DR verification and
  deleted afterward.
- The first durable key is `guardian-images` (ecdsa-p256), the Guardian
  release-signing key the image countersigner signs with. Its stakes are
  lower than an encryption key's —
  loss means re-key and re-sign the estate, not data loss — but its material
  must equally survive reinits (fresh material would orphan every existing
  countersignature). The standing
  `guardian-countersigner` policy grants sign plus public-key read only. The
  key must never be casually rotated: cosign's transit verification assumes a
  fixed key version, and old countersignatures verify only against the version
  that made them.

## Auth & self-init config
- Kubernetes auth method; ESO and the writer path authenticate via SA tokens validated
  by TokenReview, bound to namespace + SA + audience, least-privilege policies.
- **Self-init is the sole source of truth for steady-state OpenBao config.** The `initialize`
  block runs once, at first cluster initialization, with a temporary privileged token OpenBao
  revokes immediately afterward. In that one block it creates the complete steady state: the
  Kubernetes auth method + config, the `kv` (v2) and `transit` engines and their tunes, and one
  reader+writer policy/role pair per consumer namespace. There is no reconciling operator and no
  hand-authored operation CRs — a cold boot converges the same config natively, and there is no
  operator-held privileged token in the steady-state path.
- **Access is scoped per namespace subtree, not per secret path.** Each consumer namespace gets
  `guardian-reader-<ns>` (ESO, SA `secrets-reader`, read-only on
  `kv/guardian/guardian-mgmt/<ns>/*`) and `guardian-writer-<ns>` (SA `secrets-writer`,
  short-TTL, write-only within the same subtree; the platform-agent identity mints its
  token through a TokenRequest grant scoped to those SAs, so writes are headless while
  reads stay structurally out of reach). This makes OpenBao config O(1) in the number of integrations: a new secret in an
  existing namespace is a Git-only change (ExternalSecret + workload) plus one scoped value
  write via the official CLI — no policy edit, no re-init. A writer token for one stage
  physically cannot write another stage's paths, so prod/gamma mixups are prevented by the
  server, not by care. `TestOpenBaoSecretScopeConformance` enforces the same invariant at
  review time over every ExternalSecret and SecretStore in the repo.
- Only structural config changes (a new consumer namespace, a new mount or auth method) touch
  the `initialize` block, and they ship as Git edit + re-initialization (raft-snapshot
  restore under the new config, zero-downtime for consumers because
  materialized Secrets are Orphan/Retain). Routine integration adds never re-init.
- ESO is the consumer: SecretStore/ClusterSecretStore → OpenBao KV; ExternalSecrets
  materialize native Secrets. OpenBao is source of truth; the k8s Secret is a synced cache. The
  `external-dns` ExternalSecret going Ready is the functional proof that self-init created the
  kv mount and auth role and ESO can read them.

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
- Primary recovery model: config rebuilds from Git (self-init), data restores
  from the latest raft snapshot. A cluster CronJob runs
  `bao operator raft snapshot` and ships the result to R2 continuously; the
  snapshot is barrier-encrypted (unreadable without the custody-held static
  seal key, so offsite storage violates no boundary) and carries every KV
  version and Transit keyring in one artifact.
- **No recovery keys.** Self-init emits no root token and no recovery keys, and
  none are established afterward. Recovery keys cannot decrypt the barrier;
  they only authorize `generate-root`. Break-glass admin access is regenerate:
  wipe, cold-start from Git, restore the snapshot.
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
self-init's `initialize` block creates Kubernetes auth, the KV and Transit engines,
and the per-namespace reader/writer policies + auth roles in one shot → ESO consumes. Flux ordering must
be real (cert-manager Ready → listener CA Certificate Ready → listener leaf Certificate
Ready → OpenBao pods → the external-dns ExternalSecret Ready); `disableWait` makes this
easy to race, so encode the dependency explicitly.

---

## Remaining Ops Inventory

Created by the self-init `initialize` block and Ready in the current cluster:
- `kv` mount and tune.
- `transit` mount and tune.
- Kubernetes auth backend and tune.
- Per-namespace `guardian-reader-<ns>`/`guardian-writer-<ns>` policies and Kubernetes auth
  roles (the scoped-namespace list lives in the self-init block, pinned by
  `TestOpenBaoOperationsInventoryConformance`).
- The `guardian-countersigner` policy and role (transit sign + key read for the image
  countersigner's SA).

Declared alongside self-init:
- `ClusterSecretStore/external-dns-openbao` and `ExternalSecret/cloudflare-external-dns`.

Legacy live cruft removed on 2026-06-29:
- Deleted `tenant-root/ExternalSecret guardian-cnpg-backup-creds`,
  `tenant-root/ExternalSecret guardian-clickhouse-backup-creds`,
  `tenant-root/SecretStore openbao`, and `tenant-root/SecretStore openbao-clickhouse-backup`.
  They pointed at retired `http://openbao-guardian.tenant-root.svc:8200`. Database backups
  should remain platform-managed (Cozystack backup machinery pointed at off-cluster R2)
  unless we intentionally reintroduce an OpenBao-backed credential projection.

Ops resources still needed before OpenBao is the real vault/transit authority:
- Restore drill for any durable Transit key before that key protects production
  ciphertext or signatures anything verifies. For `guardian-images`: restore
  the latest raft snapshot into a throwaway OpenBao, sign a test digest, and
  assert the public key matches the production key's fingerprint.
- Durable Transit-key provisioning only when a real consumer requires it.
  Self-init declares mounts/policies/auth roles; durable key material is
  data — created imperatively once, recovered through snapshot restore —
  never a self-init request (re-runs would mint new material each time) and
  never a custom operator or CRD.
- ExternalSecrets for any remaining consumers of Cloudflare/R2 credentials beyond
  `external-dns` (the per-namespace roles already cover them; only the Git-side ESO wiring
  is missing).
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
