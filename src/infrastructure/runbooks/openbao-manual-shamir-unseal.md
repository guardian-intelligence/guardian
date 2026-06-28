# OpenBao Manual Shamir Unseal

Guardian tenant OpenBao intentionally uses Manual Shamir unseal as a bootstrap
exception. This is not the target steady-state automation model. It is the
temporary trust anchor for worst-case recovery while Guardian does not yet have
a suitable external KMS, HSM, KMIP, PKCS#11, or parent transit-seal authority.

Manual Shamir violates the normal unattended-operations rule by design. The
exception is limited to OpenBao initialization, unseal after full restart, and
disaster-recovery bootstrap. Do not store unseal keys or the initial root token
in Kubernetes, Git, CI, chat, shell history, or any OpenBao-backed secret path.

## Preconditions

- Flux has reconciled the intended `main` revision.
- `tenant-guardian` exists.
- `HelmRelease/tenant-guardian/guardian-openbao` is present.
- Three trusted key custodians are available for a 3-of-5 unseal quorum.
- A secure offline recording process exists for each unseal key and the initial
  root token.

## Start OpenBao

Flux keeps the OpenBao HelmRelease active. The declared StatefulSet update
strategy is `RollingUpdate`, so OpenBao configuration and image changes roll
through the raft set from the Flux-managed HelmRelease.

```sh
kubectl --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig \
  -n tenant-guardian get pods \
  -l app.kubernetes.io/name=openbao \
  -o wide
```

For a sealed pod, Kubernetes readiness can stay false until the unseal quorum is
entered. Use the pod phase and container state to pick the pod that needs
initialization or unseal; use the status drill below as the pass/fail gate after
the keys are entered.

## Initialize Once

Run initialization exactly once, from a trusted operator workstation. Capture
the output directly into the offline custody process.

```sh
kubectl --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig \
  -n tenant-guardian exec -it pod/guardian-openbao-0 -- \
  env BAO_ADDR=http://127.0.0.1:8200 \
  bao operator init \
    -key-shares=5 \
    -key-threshold=3
```

Distribute the five unseal keys across custodians. Keep the initial root token
under separate break-glass custody. Do not place any of this material in a
Kubernetes Secret.

## Unseal

Every sealed OpenBao pod needs three valid unseal key submissions. Repeat this
for each sealed pod.

```sh
kubectl --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig \
  -n tenant-guardian exec -it pod/guardian-openbao-0 -- \
  env BAO_ADDR=http://127.0.0.1:8200 \
  bao operator unseal
```

Run the same command for `guardian-openbao-1` and `guardian-openbao-2` when
they are sealed. Each invocation prompts for one unseal key.

For an image or configuration rollout, watch each StatefulSet replacement pod
and unseal it before the next ordinal needs quorum capacity.

## Verify

```sh
aspect infra openbao-drill \
  --mode status \
  --revision "$(git rev-parse HEAD)" \
  --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig
```

Pass criteria:

- `Initialized` is `true` for every pod.
- `Sealed` is `false` for every pod.
- Every pod reports the OpenBao version declared by the StatefulSet template.
- One pod is the active raft leader.

When a root token is needed for bootstrap configuration, provide it only through
the operator environment:

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

## Rotation And Loss

After bootstrap, create durable admin auth and narrowly scoped operational
policies. Revoke or rotate the initial root token once those paths are verified.

If fewer than three unseal keys remain available, treat this as a recovery
incident. Rotate the Shamir key set with a quorum before another outage. If
quorum is permanently lost, the encrypted OpenBao data is unrecoverable without
restoring from a snapshot that still has a valid quorum.

## Exit Criteria For This Exception

Replace Manual Shamir when Guardian has a suitable external trust anchor:

- parent OpenBao transit seal backed by an independently recoverable root;
- KMIP, HSM, or PKCS#11 device;
- cloud KMS from an accepted provider.

R2 is S3-compatible object storage and is valid for OpenTofu state and backups.
It is not a KMS seal authority.
