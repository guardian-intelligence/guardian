# Custody bundle lifecycle

The custody bundle is secret-zero: the set of credentials that controls the
company and that no system the cluster controls may ever hold in full. Since
`aspect infra custody` shipped, the **encrypted restic repository is the only
at-rest form** of these files. Plaintext exists in exactly one place — the
tmpfs bundle directory `/dev/shm/guardian-custody` — and only between an
explicit `restore` and an explicit `wipe`. The repo tree and the operator
home hold no plaintext custody material; the pre-commit and CI secret scans
treat any custody-shaped file inside the workspace as a failure, gitignored
or not.

## What is in the bundle

The manifest lives in `src/infrastructure/cmd/custody/main.go` and `create`
fails closed: a bundle missing any required member is refused, because a
bundle that would be trusted and useless is worse than none.

| bundle path | required | what it is |
| --- | --- | --- |
| `talm/secrets.yaml` | yes | Talos genesis secrets (machine/k8s/etcd CAs, service-account keys) |
| `talm/talm.key` | yes | age key decrypting the `.encrypted` Talm variants |
| `talm/talosconfig` | yes | Talos API client credentials |
| `openbao/unseal-<sha256>.key` | yes | OpenBao static-seal key; content hash must match the filename fingerprint |
| `openbao/metadata.json` | yes | seal-key metadata |
| `custody.env` | yes | operator env keys (importer source of truth) |
| `keys/*`, `latitude.token` | no | provider keys — re-issuable through consoles, but their presence sets DR speed |

Not yet in the manifest: OpenBao `transit/backup` keyring exports — none
exist (no durable Transit consumer ships yet). The cold-boot runbook already
gates the first durable Transit key on custody replication being in place;
that PR must add the export as a required manifest member here.

Deliberately excluded: minted kubeconfigs (re-derivable from `talosconfig`,
and including them would replicate live credentials), the `.encrypted` Talm
variants (ciphertext derivable from the plaintext + `talm.key`), drill logs,
and the custody README (it is the *locations* log and must stay readable
without the bundle password).

## Lifecycle

All actions run as `aspect infra custody --action <name>`.

- **create** — resolves the manifest (from the open tmpfs bundle if one
  exists, else from the legacy plaintext locations during
  migration/genesis), stages on tmpfs, backs up into the repository
  (`~/.guardian/custody/repo`), runs `restic check`, then closes the loop
  itself: restores the fresh snapshot to a scratch dir, byte-compares every
  member against its source, and only on a proven round trip shreds the
  plaintext sources (including the talm root's minted kubeconfig and
  `.encrypted` residue). A failed proof leaves every source untouched. Run it after **every custody event**:
  seal-key rotation, operator-key change, importer env change, CA rotation,
  new provider key.
- **verify** — repository integrity plus a fail-closed check that the latest
  snapshot carries every required member. With `--read-data` it re-reads
  every pack; run that form against each offline copy where it lives
  (`--repo /media/<usb>/guardian-custody-repo`).
- **restore** — puts the latest snapshot back at `/dev/shm/guardian-custody`
  and refuses to restore over an open bundle. This is how breakglass and DR
  get their inputs.
- **wipe** — overwrites and removes the tmpfs bundle. Run it the moment the
  operation that needed plaintext is done. `status` nags while a bundle is
  open.
- **status** — latest-snapshot age (warns past 30 days), open-bundle and
  plaintext-residue warnings.
- **key-add** — adds a second repository password.

## Passwords

The repository password is the whole story; restic has no backdoor and we
run no escrow. Keep **two** keys on the repository:

1. The primary, known to the operator.
2. A second key (`--action key-add`) stored in a password manager that has
   an account-recovery flow. Either key alone unlocks the repository.

Both lost means: cluster alive → rescue through the OIDC admin plane
(cluster-admin can still read in-cluster state and re-export what the
cluster holds); cluster dead → reimage and restore from R2, forfeiting
OpenBao contents. That is the loss-math row you chose to live with — see
`cold-boot-bootstrap.md` § Custody replication.

## Replication (the operator's half)

After every `create`:

1. Copy the repository to at least two offline media:
   `cp -a ~/.guardian/custody/repo /media/<usb>/guardian-custody-repo`
2. Verify each copy where it lives:
   `aspect infra custody --action verify --read-data --repo /media/<usb>/guardian-custody-repo`
3. Store the media in two physical locations, neither the cluster's
   datacenter, never co-located with raft snapshots, R2 credentials, or
   anything else the bundle's keys can decrypt.
4. Record locations and refresh dates — never contents — in
   `~/guardian-custody/README.md`.

## Genesis (new cluster from this repo)

`talm gen secrets` mints a fresh secrets bundle; the OpenBao static-seal key
comes from `openbao_static_seal_key`; provider keys come from the consoles.
Lay them out (or let the legacy resolution find them), run
`aspect infra custody --action create`, and do the replication steps before
the first workload ships. A cluster whose custody exists in one place is one
disk failure away from the bad row of the loss table.

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
  then `create` a new snapshot and refresh both offline copies. Rehearse
  this before it is needed — it is a drill like any other.
- **the unseal key** — rotate the static seal per
  `openbao-static-seal-self-init.md`, then re-bundle.
- **`custody.env` keys, provider keys** — revoke and re-issue in the owning
  console (Cloudflare, GitHub, Latitude), re-import via the importer plan,
  re-bundle.
- **the restic repository password** — `restic key add` a new key, `restic
  key remove` the exposed one, and re-copy the repository to the offline
  media (old media still honor the removed key's ciphertext — refresh them).

In every case, finish by writing down what leaked, when, and what was
rotated in `~/guardian-custody/README.md`.
