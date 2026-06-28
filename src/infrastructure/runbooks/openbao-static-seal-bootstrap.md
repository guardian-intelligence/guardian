# OpenBao Static Seal Bootstrap

Guardian tenant OpenBao uses OpenBao static seal. The static seal key is a
32-byte raw key stored on the TPM-backed Talos `openbao-seal` user volume and
mounted read-only into the OpenBao pod:

- host path: `/var/mnt/openbao-seal/secrets`
- pod path: `/openbao/secrets`
- active key: `/openbao/secrets/unseal-20260628-1.key`
- key id: `20260628-1`

The key must never live in Kubernetes Secrets, container environment variables,
Git, CI, chat, shell history, OpenBao KV, or OpenBao-backed secret paths. Talos
owns the at-rest protection for the host file. OpenBao only reads the file.

## Preconditions

- Flux has reconciled the intended `main` revision.
- `tenant-guardian` exists.
- `HelmRelease/tenant-guardian/guardian-openbao` is present.
- The OpenBao HelmRelease contains `seal "static"` and `current_key =
  "file:///openbao/secrets/unseal-20260628-1.key"`.
- The three declared control-plane nodes carry node label
  `guardian.dev/openbao-seal=true`.
- The OpenBao StatefulSet has required node affinity for both
  `guardian.dev/openbao-seal=true` and hostnames `ash-earth`, `ash-wind`, and
  `ash-water`; it also has required pod anti-affinity across
  `kubernetes.io/hostname`.
- Every labeled node has the `UserVolumeConfig/openbao-seal` volume mounted at
  `/var/mnt/openbao-seal`.
- Every labeled node has the same 32-byte key at
  `/var/mnt/openbao-seal/secrets/unseal-20260628-1.key`.
- The key file is readable by the OpenBao pod without broad permissions: owner
  UID `100` mode `0400`, or group `1000` mode `0440`.
- The `openbao-seal` user volume is encrypted with Talos LUKS2 TPM key slot 0.

## Node Linkage

The key-bearing nodes and the schedulable OpenBao nodes must be the same set.
Today that set is:

- `ash-earth`
- `ash-wind`
- `ash-water`

Do not add `guardian.dev/openbao-seal=true` to a node until its
`UserVolumeConfig/openbao-seal` is ready and the current static seal key is
present. Do not place the key on an unlabeled node and then rely on scheduler
luck. Adding a fourth node also requires an explicit HelmRelease hostname
change; the label alone is not enough to make a node eligible.

## Key Placement

Create the seal key with the approved break-glass entropy process. The key is
exactly 32 bytes; do not base64-wrap it unless the bytes written to the node are
the decoded 32-byte key.

Place the identical key file on each labeled control-plane node through an
approved break-glass node-configuration path after the `openbao-seal` user
volume is ready. Do not commit the key or a Secret manifest. Verify only
metadata and fingerprints through a transient hostPath-mounted diagnostic pod
or equivalent Talos-controlled path:

```sh
sha256sum /var/mnt/openbao-seal/secrets/unseal-20260628-1.key
wc -c /var/mnt/openbao-seal/secrets/unseal-20260628-1.key
```

All nodes must report the same SHA-256 fingerprint and a size of `32`.

## Initialize Once

Run initialization exactly once, from a trusted operator workstation after the
static seal key is present on every eligible node. Static seal is auto-unseal, so
initialization produces recovery keys and the initial root token.

```sh
kubectl --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig \
  -n tenant-guardian exec -it pod/guardian-openbao-0 -- \
  env BAO_ADDR=http://127.0.0.1:8200 \
  bao operator init \
    -recovery-shares=5 \
    -recovery-threshold=3
```

Capture the recovery keys and initial root token directly into offline custody.
Do not place either in a Kubernetes Secret.

When the root token is needed for bootstrap configuration, provide it only
through the operator environment:

```sh
export BAO_TOKEN='<initial-root-token-from-break-glass-custody>'
```

Then run the scoped bootstrap configuration tooling. This is the one-time bridge
that creates the ops-controller policy and Kubernetes auth role; normal OpenBao
state convergence belongs to Flux-applied CRs and the OpenBao ops controller.

```sh
aspect infra openbao-bootstrap \
  --mode apply \
  --revision "$(git rev-parse HEAD)" \
  --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig

unset BAO_TOKEN VAULT_TOKEN
```

Do not persist the root token after the bootstrap window.

## Verify

```sh
aspect infra openbao-drill \
  --revision "$(git rev-parse HEAD)" \
  --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig
```

Pass criteria:

- `Initialized` is `true` for every pod.
- `Sealed` is `false` for every pod.
- Every pod reports the OpenBao version declared by the StatefulSet template.
- One pod is the active raft leader.

For image or configuration rollouts, keep the StatefulSet `OnDelete` strategy
and replace one pod at a time. The static seal file lets restarted pods unseal
without entering recovery keys.

## Disaster Recovery

OpenBao data is not a Guardian disaster-recovery restore target. If OpenBao raft
data, the static seal key, or recovery quorum is permanently lost, rebuild
OpenBao from the Git-declared runtime and ops-controller CRs. Then regenerate or rotate downstream secrets at their sources and let External Secrets re-project the new values.

Do not add an OpenBao snapshot-age gate or an OpenBao backup-restore runbook.
Snapshot restore is not the production recovery path for Guardian tenant
OpenBao.

## Rotation

Static seal rotation is a deliberate maintenance operation:

- place the new 32-byte key on every eligible node;
- add `previous_key_id` and `previous_key` in the OpenBao HCL while moving the
  new key to `current_key_id` and `current_key`;
- roll one OpenBao pod at a time;
- keep the previous key until OpenBao seal migration or rewrap evidence proves
  the old key is no longer needed.

Never rotate by changing the file contents without changing the key id.
