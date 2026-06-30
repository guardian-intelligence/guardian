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
and that its SHA-256 fingerprint equals the declared `current_key_id`. The node
directory must be mode `0700` or `0750`; the key file must be mode `0400`,
`0440`, `0600`, or `0640`, owned by root or a narrowly scoped OpenBao runtime
user/group. Do not leave the directory or key world-readable.

The static seal file is the security boundary for this deployment. Node/root compromise on a
key-bearing node is an OpenBao compromise. The key is accepted as a long-term production
posture only with dedicated tainted OpenBao nodes, strict hostPath admission controls,
separate backup custody, and rotation runbooks that retain old keys for as long as any
retained raft snapshot needs them.

## Startup

Flux reconciles:

- cert-manager's bootstrap Issuer and Certificate for initial OpenBao API TLS;
- `HelmRelease/tenant-guardian/guardian-openbao`;
- the OpenBao ops-controller CRDs and Deployment;
- Flux-applied OpenBao operation CRs.

On first startup, exactly one raft member initializes the cluster and runs the
OpenBao `initialize` block. The other raft members join an already initialized
cluster. Self-init creates only the minimum needed for steady state:
Kubernetes auth, its config, the ops-controller policy, and the ops-controller
role. The temporary privileged token used internally by self-init is not
returned and is revoked by OpenBao after initialization.

The bootstrap cert-manager self-signed/CA issuer path exists only to create the
`guardian-openbao-api-tls` Secret before the OpenBao pod can mount it. After
OpenBao is initialized, steady state must move the OpenBao API leaf to a
cert-manager Vault issuer backed by OpenBao PKI, then remove the bootstrap
issuer/CA path after trust overlap.

## PKI Handoff

Target shape:

- Flux declares `OpenBaoMount/pki-openbao-api`.
- Flux declares `OpenBaoPKIRootIssuer/openbao-api-root-2026`; OpenBao generates the CA
  private key internally and sets the issuer as the mount default. No CA private key goes in
  Kubernetes, Git, CI, or a local operator file.
- Flux declares `OpenBaoPKIRole/openbao-api`, which can sign only the OpenBao API
  listener names already present in `openbao-pki.yaml`.
- Flux declares the cert-manager policy with only `update` on
  `pki/openbao-api/sign/openbao-api`.
- cert-manager authenticates to OpenBao through Kubernetes auth with
  `ServiceAccount/cert-manager-openbao-issuer` and a short-lived projected token.
- `Issuer/guardian-openbao-vault` points at
  `https://guardian-openbao.tenant-guardian.svc:8200`, path
  `pki/openbao-api/sign/openbao-api`, and the OpenBao API CA bundle.

Handoff order:

1. Keep the bootstrap `guardian-openbao-api-tls` Secret serving OpenBao during first
   come-up.
2. Converge the OpenBao PKI mount, role, policy, and Kubernetes auth role.
3. Converge `OpenBaoPKIRootIssuer/openbao-api-root-2026` and wait for `Ready=True`.
4. Create the cert-manager Vault Issuer and wait for `Ready=True`.
5. Keep `Certificate/guardian-openbao-api` pointed at `Issuer/guardian-openbao-vault`
   so the existing `guardian-openbao-api-tls` Secret is renewed by OpenBao PKI.
6. Verify the SIGHUP sidecar reloads the OpenBao-issued leaf and all three OpenBao pods remain
   unsealed in one raft cluster.
7. Remove the bootstrap self-signed issuer/CA Certificate only after the new CA
   is trusted everywhere that talks to the OpenBao API.

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
