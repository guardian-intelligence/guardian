# OpenBao Residue Inventory And DR Gates

Status: target-state inventory for Guardian OpenBao cold-start and stateful
restore verification.

This inventory separates state that can be rebuilt from Git from custody
residue that must be supplied out of band. OpenBao is not the root of trust for
its own listener certificate. cert-manager owns that transport certificate
through an independent listener CA; OpenBao owns secret custody after it starts.

## Residue Inventory

| Artifact | Classification | Durable home | Loss impact | Notes |
| - | - | - | - | - |
| Static seal key and key id | Data-loss-critical | Offline custody, placed out of band on the three dedicated OpenBao nodes | OpenBao raft snapshots and retained data cannot be unsealed | Keep old key versions for as long as any snapshot encrypted under them is retained. |
| Recovery keys | Intentionally not established | None | Break-glass admin access is regenerate: wipe, cold-start from custody residue, restore durable Transit keys from their exported backups | Self-init emits no root token and no recovery keys. Recovery keys cannot decrypt the barrier; they only authorize `generate-root`, which the regenerate model replaces. |
| Cloudflare ExternalDNS token | Reimportable custody residue | `custody.env` only during bootstrap import; then OpenBao KV path `kv/guardian/guardian-mgmt/external-dns/cloudflare` | DNS reconciliation stops until reimported | `custody.env` lives in the encrypted custody bundle; after import the operator wipes the restored plaintext (`aspect infra custody --action wipe`). |
| Cloudflare DNS load-balancer provisioner token | Reimportable custody residue | Off-cluster break-glass or CI secret store; imported to `kv/guardian/guardian-mgmt/operator/cloudflare` only when needed by an OpenBao-backed consumer | Edge provisioning and recovery workflows cannot run until supplied | Keep out of Kubernetes steady state unless a real consumer exists. |
| Cloudflare R2 state/backend credentials | Reimportable custody residue | Off-cluster break-glass or CI secret store; imported to `kv/guardian/guardian-mgmt/operator/r2` only when needed by an OpenBao-backed consumer | OpenTofu state or backup workflows cannot run until supplied | R2 backup custody is distinct from OpenBao seal-key custody. |
| Transit keys protecting durable ciphertext | Data-loss-critical | OpenBao barrier plus a `transit/backup` plaintext keyring export in offline custody | Losing the key is data loss for that ciphertext | Create with `exportable=true` and `allow_plaintext_backup=true`; export at creation. The export is the full plaintext keyring: anyone holding it can decrypt everything under that key. Offline custody only, same tier as the static seal key — never R2, never co-located with the ciphertext it protects, snapshots, or R2 credentials. |
| OpenBao raft snapshot | Optional second recovery path | Backup custody, eventually R2 after restore-drill and Velero research | None on its own; the primary recovery for durable Transit keys is the `transit/backup` export | Barrier-encrypted: unreadable without the static seal key, so offsite storage is acceptable. Restore requires the same static seal key; still handle as sensitive. |
| Offline workload PKI root | Data-loss-critical once workload PKI exists | Offline CA custody outside Kubernetes and OpenBao | Workload PKI trust cannot be re-created under the same root | Not needed until a workload PKI consumer exists. |

## Rebuildable From Git

- cert-manager controller installation, through the platform bundle.
- Independent cert-manager listener CA resources for
  `guardian-openbao-api-tls`.
- OpenBao HelmRelease, static-seal configuration, storage, TLS volume mount,
  and the self-init `initialize` block.
- KV mount, Transit mount, Kubernetes auth backend, the external-dns policy and
  auth role — all created by the self-init block (there is no custom operator,
  CRDs, or hand-authored operation CRs).
- External Secrets Operator stores and ExternalSecrets.

## Cold-Start DR Gate

Run this without R2/OpenBao snapshots. It proves the Git-declared system can
start from scratch using only custody-held residue.

1. Wipe or recreate the OpenBao environment.
2. Bring up cert-manager first and verify the controller is running.
3. Verify the independent cert-manager listener issuer creates
   `guardian-openbao-api-tls`.
4. Place the static seal file only on the dedicated tainted OpenBao nodes.
5. Start OpenBao and verify self-init: exactly one member initializes and all
   three members report the same non-empty raft `cluster_id`.
6. Verify OpenBao operation resources converge.
7. Import only custody-held bootstrap secrets needed for live integrations.
8. Wipe the restored custody bundle after successful write/readback and
   importer role cleanup (`aspect infra custody --action wipe`); verify no
   plaintext `custody.env` remains anywhere.
9. Verify ESO syncs a real consumer, currently ExternalDNS.
10. Verify Cloudflare DNS reconciliation works.
11. Verify Transit encrypt/decrypt with a non-durable test key.
12. Verify workload PKI only after a real workload PKI consumer exists.

## Stateful Restore DR Gate

Run this separately from cold-start. It proves durable OpenBao state can survive
loss of the running cluster.

1. Create a sentinel Transit key that models durable usage: `exportable=true`,
   `allow_plaintext_backup=true`.
2. Encrypt a known plaintext and store the ciphertext outside OpenBao.
3. Export the keyring with `transit/backup` into custody. Optionally also take
   a raft snapshot to exercise the second path.
4. Cold-start a throwaway OpenBao cluster and restore the key with
   `transit/restore`.
5. Verify the old ciphertext decrypts.
6. If exercising the snapshot path, restore the snapshot into a throwaway
   cluster started with the same static seal key and repeat the decrypt check.
7. Verify KV or workload PKI sentinels only if those engines contain durable
   production state.

Snapshot automation stays deferred pending Velero research, and the
`transit/backup` export makes it non-load-bearing: manual raft snapshots are an
optional second copy, handled as sensitive custody material rather than
ordinary backups.
