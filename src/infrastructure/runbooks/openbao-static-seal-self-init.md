# OpenBao Static Seal And Self-Init

Guardian OpenBao in `tenant-guardian` uses OpenBao static auto-unseal and
OpenBao self-init. It does not use an operator-driven unseal runbook or a
separate OpenTofu root in the happy path.

## Seal Key

Generate the 32-byte static seal key with the repo-pinned Bazel target into an
operator-chosen custody directory:

```sh
bazelisk run //src/infrastructure/cmd/openbao_static_seal_key:openbao_static_seal_key -- \
  --cluster guardian-mgmt \
  --region ash \
  --out-dir /secure/offline-custody/openbao/guardian-mgmt-ash/static-seal
```

The command uses the key's SHA-256 fingerprint as `current_key_id`. Do not print
or copy the key into Git, Kubernetes Secrets, CI, chat, shell history, Talos
`machine.files`, or OpenBao paths. The key must be placed out of band on each
OpenBao key-bearing node at:

```text
/var/lib/guardian/openbao/static-seal/unseal-<current_key_id>.key
```

The OpenBao pod init container verifies that the mounted key is exactly 32 bytes
and that its SHA-256 fingerprint equals the declared `current_key_id`. For the
pinned OpenBao image, use `0750 root:1000` on the node directory and `0440
root:1000` on the key file:

```text
drwxr-x--- root 1000 /var/lib/guardian/openbao/static-seal
-r--r----- root 1000 /var/lib/guardian/openbao/static-seal/unseal-<current_key_id>.key
```

`0700` is acceptable only if the directory owner is the OpenBao runtime user.
Do not leave the directory or key world-readable.

The static seal file is the security boundary for this deployment. Node/root compromise on a
key-bearing node is an OpenBao compromise. The key is accepted as a long-term production
posture only with dedicated tainted OpenBao nodes, strict hostPath admission controls,
separate backup custody, and rotation runbooks that retain old keys for as long as any
retained raft snapshot needs them.

## Startup

Flux reconciles the steady state:

- cert-manager's independent listener CA and OpenBao API listener Certificate;
- `HelmRelease/tenant-guardian/guardian-openbao`.

There is no custom operator, no CRDs, and no hand-authored operation CRs. On
first startup, exactly one raft member initializes the cluster and runs the
OpenBao `initialize` block; the other raft members join an already initialized
cluster. Self-init is the sole source of truth for OpenBao configuration and
creates the complete steady state directly: the Kubernetes auth method and its
config, the `kv` (v2) and `transit` engines and their tunes, the `external-dns`
read policy and Kubernetes auth role, and a temporary `guardian-secret-importer`
policy + role for the bootstrap importer. The temporary privileged token used
internally by self-init is not returned and is revoked by OpenBao after
initialization. Because `initialize` runs only at first initialization, config
added later is applied imperatively against the running cluster.

The cert-manager listener CA is steady state, not a temporary bootstrap issuer.
It creates `guardian-openbao-api-tls` before the OpenBao pod mounts the Secret.
OpenBao PKI is not used for OpenBao's own listener certificate.

## Listener TLS And Workload PKI

Target shape:

- Flux declares `Issuer/guardian-openbao-listener-selfsigned`.
- Flux declares `Certificate/guardian-openbao-listener-ca`, stored in
  `Secret/guardian-openbao-listener-ca-tls`.
- Flux declares `Issuer/guardian-openbao-listener-ca`.
- Flux declares `Certificate/guardian-openbao-api`, stored in
  `Secret/guardian-openbao-api-tls` and mounted by the OpenBao pod.
- The listener CA is transport identity only. Kubernetes/cert-manager
  compromise can mint a listener cert but cannot unseal OpenBao or read OpenBao
  state.

There is no OpenBao PKI handoff for the listener. If workload PKI is needed
later, add it as workload PKI only: an offline-held root outside Kubernetes and
OpenBao, an OpenBao-held intermediate, a workload-specific mount such as
`pki/workload`, and a cert-manager Vault issuer limited to the approved
`sign/<role>` path.

## Verify

```sh
aspect infra converged \
  --expected-revision "$(git rev-parse HEAD)" \
  --kubeconfig=src/infrastructure/talm/kubeconfig

aspect infra openbao-drill \
  --kubeconfig=src/infrastructure/talm/kubeconfig
```

The converged proof requires every declared Flux Kustomization to be Ready at
the expected revision. Component health gates Kustomization readiness through
Flux health checks declared in the manifests: `guardian-system` waits on the
listener Certificates, HelmRelease, and StatefulSet; `guardian-mgmt-dns-controller`
waits on its `ClusterSecretStore` and `ExternalSecret` reporting `Ready=True` via
`healthCheckExprs`. That ExternalSecret only goes Ready once self-init has created
the kv mount and the external-dns auth role and ESO can read them, so it is the
functional proof that self-init succeeded. The status drill verifies each member
is initialized, unsealed, HA-enabled, and part of one raft cluster (a single
`cluster_id` across pods).
If the external-dns ExternalSecret never goes Ready, the cluster likely did not
run the declared self-init block successfully; inspect OpenBao startup logs and,
if the raft state was wiped, recreate it with the declared config.

## Bootstrap Secret Import

One-time imports from local operator files such as `DELETE_ME.env` must happen
after the new OpenBao cluster is initialized and the `kv` mount has converged.
Do not encode those values in Git or Kubernetes manifests. This command is a
bootstrap-only, heavily cordoned, non-load-bearing tool; do not use it as a
routine secret-management path.

```sh
bazelisk run //src/infrastructure/cmd/openbao_secret_import:openbao_secret_import -- \
  --kubectl "$(bazelisk info output_base)/external/+http_file+kubectl_linux_amd64/file/kubectl" \
  --kubeconfig "$HOME/.kube/config" \
  --env-file DELETE_ME.env
```

The importer authenticates through the temporary `guardian-secret-importer`
Kubernetes auth role created by self-init. It writes:

- `kv/guardian/guardian-mgmt/tenant-guardian/dns/external-dns`
- `kv/guardian/guardian-mgmt/operator/cloudflare`
- `kv/guardian/guardian-mgmt/operator/r2`

After successful write and readback verification, the importer deletes the
temporary OpenBao auth role and policy, then deletes the local import file.
