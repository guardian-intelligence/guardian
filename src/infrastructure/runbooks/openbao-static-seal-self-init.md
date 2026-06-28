# OpenBao Static Seal And Self-Init

Guardian OpenBao in `tenant-guardian` uses OpenBao static auto-unseal and
OpenBao self-init. It does not use an operator-driven unseal runbook or a
separate OpenTofu root in the happy path.

## Seal Key

Generate the 32-byte static seal key with the repo-pinned Bazel target:

```sh
bazelisk run //src/infrastructure/cmd/openbao_static_seal_key:openbao_static_seal_key -- \
  --cluster guardian-mgmt \
  --region ash
```

The command writes the key under `~/.guardian/openbao/guardian-mgmt-ash` and
uses the key's SHA-256 fingerprint as `current_key_id`. Do not print or copy the
key into Git, Kubernetes Secrets, CI, chat, shell history, or OpenBao paths. The
key must be placed out of band on each OpenBao key-bearing node at:

```text
/var/lib/guardian/openbao/static-seal/unseal-<current_key_id>.key
```

The OpenBao pod init container verifies that the mounted key is exactly 32 bytes
and that its SHA-256 fingerprint equals the declared `current_key_id`.

## Startup

Flux reconciles:

- cert-manager Issuer and Certificate for OpenBao API TLS;
- `HelmRelease/tenant-guardian/guardian-openbao`;
- the OpenBao ops-controller CRDs and Deployment;
- Flux-applied OpenBao operation CRs.

On first startup, exactly one raft member initializes the cluster and runs the
OpenBao `initialize` block. The other raft members join an already initialized
cluster. Self-init creates only the minimum needed for steady state:
Kubernetes auth, its config, the ops-controller policy, and the ops-controller
role. The temporary privileged token used internally by self-init is not
returned and is revoked by OpenBao after initialization.

## Verify

```sh
aspect infra openbao-cutover \
  --expected-revision "$(git rev-parse HEAD)" \
  --kubeconfig=src/infrastructure/talm/kubeconfig
```

The cutover proof requires Flux Kustomizations, the OpenBao HelmRelease,
StatefulSet, ops-controller Deployment, and OpenBao operation CRs to be ready.
`SelfInitIncomplete` on OpenBao operation CRs means the cluster did not run the
declared self-init block successfully; inspect OpenBao startup logs and
recreate the wiped OpenBao raft state with the declared config.

## Secret Import

One-time imports from local operator files such as `DELETE_ME.env` must happen
after the new OpenBao cluster is initialized and the `kv` mount has converged.
Do not encode those values in Git or Kubernetes manifests.

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
