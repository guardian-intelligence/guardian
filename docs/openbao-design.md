# OpenBao secrets platform — design

Guardian runs a three-member OpenBao raft cluster with static auto-unseal,
self-init, independent cert-manager listener TLS, retained encrypted local
storage, KV, and Transit. OpenBao configuration lives in the self-init
`initialize` block; the tested platform convergence proof is
`aspect infra converged`.

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
- **Encrypted local storage per member** — `local-encrypted-retain` uses
  Cozystack's native LINSTOR `luks storage` layer with remote access disabled.
  Each member has one retained LUKS PVC for raft data and one for audit data.
  Raft already replicates at the application layer; DRBD underneath would
  multiply physical copies, add a network hop to the latency-critical per-entry
  `fsync`, and introduce double-attach/fencing risk a consensus store exists to
  avoid.
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
  durable ciphertext is data-loss-critical: create it with `exportable=true`
  and `allow_plaintext_backup=true`, export the full keyring exactly once through
  `transit/backup`, and seal that export in offline custody. The export is
  plaintext key material and must never enter Git, Kubernetes, R2, chat, argv,
  or storage colocated with OpenBao data.
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
  the store stays no-read to the agent — it holds privilege-escalating and
  customer-integration material wholesale, the never-agent-readable class under the
  read policy in `docs/secrets.md`). This makes OpenBao config O(1) in the number of integrations: a new secret in an
  existing namespace is a Git-only change (ExternalSecret + workload) plus one scoped value
  write via the official CLI — no policy edit, no re-init. A writer token for one stage
  physically cannot write another stage's paths, so prod/gamma mixups are prevented by the
  server, not by care. `TestOpenBaoSecretScopeConformance` enforces the same invariant at
  review time over every ExternalSecret and SecretStore in the repo.
- Only structural config changes (a new consumer namespace, a new mount or auth method) touch
  the `initialize` block, and they ship as Git edit + re-initialization,
  custody import, and scoped re-relays. Consumers keep running because
  materialized Secrets are Orphan/Retain. Routine integration adds never re-init.
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
- A sidecar tails the file to stdout, where the cluster log collector ingests it
  into VictoriaLogs. This path is asynchronous and is not an OpenBao audit
  device; the local file remains the request-path audit device.

## Disaster recovery
- Primary recovery model: self-init rebuilds structure from Git; the importer
  restores fixed integration inputs from custody/bootstrap storage; scoped
  writers relay in-cluster-generated Orphan/Retain Secrets; durable Transit
  keys restore from custody-held `transit/backup` keyring exports.
- **No recovery keys or standing administrator.** Self-init emits no root token
  and no recovery keys, and none are established afterward. Recovery keys
  cannot decrypt the barrier; they only authorize `generate-root`.
- **The 32-byte static seal key is a first-class custody artifact.** Every member
  needs the exact key file to start and join the raft cluster. It is stored in
  the encrypted custody repository and placed out of band on each key-bearing
  node.
- **No automated raft snapshots.** No OpenBao snapshot CronJob, uploader, or R2
  restore dependency is deployed. A manual snapshot is not a migration or DR
  prerequisite.
- **Tested recovery:** required before relying on a durable Transit key. The
  drill cold-starts a throwaway cluster, restores the exported keyring through
  `transit/restore`, and verifies a pre-recovery ciphertext or signature against
  the original public-key fingerprint.

## Seal-key rotation
- `previous_key`/`current_key` under a new key-id; roll one pod at a time and
  keep the previous key until rewrap/seal-migration evidence clears it. Key
  contents never change without the key-id changing.

---

## Bootstrap ordering

Talos up → break-glass place seal key on the 3 pinned nodes → **cert-manager up** →
independent cert-manager listener CA creates `guardian-openbao-api-tls` →
**OpenBao up** (static-seal auto-unseals; consumes the listener cert from birth) →
self-init's `initialize` block creates Kubernetes auth, the KV and Transit engines,
and the per-namespace reader/writer policies + auth roles in one shot → ESO consumes. Flux ordering must
be real (cert-manager Ready → listener CA Certificate Ready → listener leaf Certificate
Ready → OpenBao pods → the external-dns ExternalSecret Ready); `disableWait` makes this
easy to race, so encode the dependency explicitly.

---

## Operations inventory

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

The `guardian-images` signing key is created imperatively once and restored
from its custody-held keyring export. It is never a self-init request, custom
operator, or CRD. Its recovery drill restores the keyring into a throwaway
OpenBao, signs a test digest, and verifies both the signature and production
public-key fingerprint.

Database backup credentials remain platform-managed through Cozystack and the
`tenant-root/backups-r2` OpenBao projection. Release signing and other online
key operations use Transit; DNS consumes its Cloudflare API token from KV.

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
- OpenBao Transit key backup/restore: https://openbao.org/api-docs/secret/transit/#backup-key
