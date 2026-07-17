# Custody bundle lifecycle

The custody bundle is the irreducible root of trust: the members whose
*changes* require reading them, which is exactly why no write-only path can
replace it. It holds nothing routine — every other secret class lives in a
tier with an open write path (`docs/secrets.md`). The encrypted restic
repository is the only at-rest form; plaintext exists in exactly one place —
the tmpfs bundle directory `/dev/shm/guardian-custody` — and only between an
explicit `restore` and an explicit `wipe`, during a ceremony. The pre-commit
and CI secret scans treat any custody-shaped file inside the workspace as a
failure, gitignored or not.

## What is in the bundle

The manifest lives in `src/infrastructure/cmd/custody/main.go` and `create`
fails closed: a bundle missing any required member is refused, because a
bundle that would be trusted and useless is worse than none.

| bundle path | what it is |
| --- | --- |
| `talm/secrets.yaml` | Talos genesis secrets (machine/k8s/etcd CAs, service-account keys) |
| `talm/talm.key` | age key decrypting the `.encrypted` Talm variants |
| `talm/talosconfig` | Talos API client credentials |
| `linstor/master-passphrase` | LINSTOR master passphrase; losing it makes every native LUKS volume unrecoverable |
| `openbao/unseal-<sha256>.key` | OpenBao static-seal key; content hash must match the filename fingerprint |
| `openbao/metadata.json` | seal-key metadata |

Deliberately excluded: minted kubeconfigs (re-derivable from `talosconfig`,
and including them would replicate live credentials), the `.encrypted` Talm
variants (ciphertext derivable from the plaintext + `talm.key`), drill logs,
and the custody README (it is the *locations* log and must stay readable
without the bundle password). Operational values — provider keys, R2
credentials, integration secrets — live in the bootstrap set and OpenBao
(`docs/secrets.md`), never here.

## Steady state: sealed

The bundle's resting state is closed, for months at a time. It opens for
exactly two reasons, and every open pages:

- **Disaster recovery** — the restore chain in `docs/secrets.md` §Disaster
  recovery and `cold-boot-bootstrap.md`.
- **Rotation ceremonies** — CA rotation, seal-key rotation: read-modify
  operations on the root material itself.

`status` nags while a bundle is open; a bundle open outside a ceremony is an
incident.

## Lifecycle

All actions run as `aspect infra custody --action <name>`.

- **create** — stages the manifest on tmpfs, backs it up into the repository
  (`~/.guardian/custody/repo`), runs `restic check`, restores the fresh
  snapshot to a scratch dir and byte-compares every member, and only on a
  proven round trip shreds the plaintext sources. A failed proof leaves
  every source untouched. It finishes by pushing the repository to the
  custody prefix of the R2 vault bucket. Run it after every ceremony that
  changes root material.
- **verify** — repository integrity plus a fail-closed check that the latest
  snapshot carries every required member. With `--read-data` it re-reads
  every pack; run that form against each pulled copy where it lives.
- **restore** — puts the latest snapshot back at `/dev/shm/guardian-custody`
  and refuses to restore over an open bundle.
- **wipe** — overwrites and removes the tmpfs bundle. Run it the moment the
  ceremony is done.
- **status** — latest-snapshot age, open-bundle and plaintext-residue
  warnings.
- **key-add** — adds a second repository password.
- **linstor-generate** — creates the 256-bit LINSTOR master passphrase once at
  `linstor/master-passphrase` in an already restored tmpfs bundle and refuses
  to replace it. Snapshot the bundle before provisioning encrypted volumes.

## Passwords

The repository password is the whole story; restic has no backdoor and we
run no escrow. Keep **two** keys on the repository:

1. The primary, known to the operator.
2. A second key (`--action key-add`) stored in a password manager that has
   an account-recovery flow. Either key alone unlocks the repository.

Both lost means: cluster alive → rescue through the OIDC admin plane
(cluster-admin can still read in-cluster state and re-export what the
cluster holds); cluster dead → rebuild from scratch and re-issue every
credential from the owning consoles. That is the loss-math row you chose to
live with.

## Replication

R2 is the primary offsite replica: `create` pushes the encrypted repository
there, and the bucket carries object versioning with the custody prefix
excluded from every lifecycle/expiry rule. The operator's half is the pull
cadence:

1. Regularly download the repository from R2 to local offline media, so
   access survives losing the R2 account or reaching it going dark.
2. Verify each pull where it lives:
   `aspect infra custody --action verify --read-data --repo /media/<usb>/guardian-custody-repo`
3. Keep pulls physically separate from anything the bundle's keys can
   decrypt — never co-located with raft snapshots or the machines the CAs
   control.
4. Record locations and refresh dates — never contents — in
   `~/guardian-custody/README.md`.

Staleness is tolerable: the members rotate rarely, so even an old pull
usually carries the current root material. `status` warns past 30 days.

## Genesis (new cluster from this repo)

`talm gen secrets` mints a fresh secrets bundle; the OpenBao static-seal key
comes from `openbao_static_seal_key`; `linstor-generate` creates the LINSTOR
master passphrase in the staged bundle. Run
`aspect infra custody --action create` and confirm the R2 push and one local
pull before the first workload ships. A cluster whose custody exists in one
place is one disk failure away from the bad row of the loss table.

## Leak response: custody material touched Git or any external system

Deleting the commit is not a remedy. Kubernetes client certificates have no
revocation, `system:masters` cannot be constrained by RBAC, and a pushed
secret must be assumed copied the moment it left the machine — force-pushing
history only hides the evidence. The remedy is rotation, scoped by what
leaked:

- **`secrets.yaml` or `talosconfig`** — rotate both root CAs:

  ```
  talosctl rotate-ca --talosconfig <restored>/talm/talosconfig \
    --control-plane-nodes 10.8.0.11,10.8.0.12,10.8.0.13
  ```

  It dry-runs by default (add `--dry-run=false` to execute), gracefully
  rolls new Talos and Kubernetes API CAs across the nodes, and writes a new
  `talosconfig` (`-o`). Afterwards the old x509 admin kubeconfigs are dead,
  OIDC logins are untouched, and the custody bundle is stale: refresh the
  local Talm operator state, verify `aspect infra talos` and a fresh
  `aspect infra auth --platform-admin --reason "post-rotation verification"`,
  then `create` a new snapshot. Rehearse this before it is needed — it is a
  drill like any other.
- **the unseal key** — rotate the static seal per
  `openbao-static-seal-self-init.md`, then re-bundle.
- **the restic repository password** — `restic key add` a new key, `restic
  key remove` the exposed one, re-push to R2, and refresh the local pulls
  (old media still honor the removed key's ciphertext).

In every case, finish by writing down what leaked, when, and what was
rotated in `~/guardian-custody/README.md`.
