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
config, the `kv` (v2) and `transit` engines and their tunes, one
reader+writer policy/role pair per consumer namespace (each scoped to that
namespace's own `kv/guardian/guardian-mgmt/<namespace>/*` subtree), and a
temporary `guardian-secret-importer` policy + role for the bootstrap importer.
The temporary privileged token used internally by self-init is not returned
and is revoked by OpenBao after initialization.

Because access is granted per namespace subtree rather than per secret path,
OpenBao configuration is O(1) in the number of integrations: adding a secret
for an existing namespace never changes this block (see Adding An
Integration below). Only structural changes — a new consumer namespace, a new
mount or auth method — edit the block and re-initialize.

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

One-time imports from local operator files such as `custody.env` must happen
after the new OpenBao cluster is initialized and the `kv` mount has converged.
Do not encode those values in Git or Kubernetes manifests. This command is a
bootstrap-only, heavily cordoned, non-load-bearing tool; do not use it as a
routine secret-management path.

```sh
bazelisk run //src/infrastructure/cmd/openbao_secret_import:openbao_secret_import -- \
  --kubectl "$(bazelisk info output_base)/external/+http_file+kubectl_linux_amd64/file/kubectl" \
  --kubeconfig "$HOME/.kube/config" \
  --env-file custody.env
```

The importer authenticates through the temporary `guardian-secret-importer`
Kubernetes auth role created by self-init (write access to the whole
`guardian-mgmt` subtree — bootstrap and DR re-seed are the only times a
cross-namespace write credential exists, and the importer deletes its own
role and policy when it finishes). Its import plan in
`src/infrastructure/cmd/openbao_secret_import/main.go` is the DR re-seed
manifest: the authoritative list of every secret the system needs after a
raft wipe, keyed by custody env variables. It currently writes:

- `kv/guardian/guardian-mgmt/external-dns/cloudflare`
- `kv/guardian/guardian-mgmt/operator/cloudflare`
- `kv/guardian/guardian-mgmt/operator/r2`
- `kv/guardian/guardian-mgmt/tenant-root/backups-r2` (bucket-scoped
  `guardian-backups` R2 keypair in the flat-key format Cozystack's
  backupstrategy-controller consumes; ESO projects it as
  `Secret/guardian-backups-creds` in `tenant-root`)
- `kv/guardian/guardian-mgmt/company-site/promotion/github-app`
- `kv/guardian/guardian-mgmt/guardian-iam/promotion/github-app` (same App
  identity as company-site's; Kargo credentials are project-namespaced)
- `kv/guardian/guardian-mgmt/guardian-products/promotion/github-app` (same
  App identity, the products vertical's project-namespaced copy)
- `kv/guardian/guardian-mgmt/verself-runner/github-app` (the Verself Runner
  GitHub App: webhook HMAC secret, OAuth client secret, and the App private
  key, transported base64-encoded as
  `github_runner_app_prod_private_key_b64`; appId/clientId ride along as
  public identity)
- `kv/guardian/guardian-mgmt/tenant-guardian-{beta,gamma,prod}/keycloak/github-oauth`
  (optional per stage: imported only when the env file carries that stage's
  `<STAGE>_GITHUB_CLIENT_SECRET`)

GitHub App private keys live in custody as PEM files and travel in the env
file base64-encoded (the file is line-oriented): the `guardian-promotions`
key as `github_promotions_app_private_key_b64` and the Verself Runner key as
`github_runner_app_prod_private_key_b64`. Build the import file as a working
copy from custody without printing any value, then pass it via `--env-file`:

```sh
aspect infra custody --action restore    # plaintext bundle at /dev/shm/guardian-custody
umask 077
B=/dev/shm/guardian-custody
cp "$B/custody.env" "$B/import.env"
printf 'github_promotions_app_private_key_b64=%s\n' \
  "$(base64 -w0 < "$B/keys/github-promotions-app.private-key.pem")" >> "$B/import.env"
printf 'github_runner_app_prod_private_key_b64=%s\n' \
  "$(base64 -w0 < "$B/keys/verself-runner.private-key.pem")" >> "$B/import.env"
# then run the import command above with:
#   --env-file /dev/shm/guardian-custody/import.env -delete-env-file
aspect infra custody --action wipe       # the moment the import verifies
```

After successful write and readback verification, the importer deletes the
temporary OpenBao auth role and policy, then (with `-delete-env-file`)
deletes the working copy; the wipe removes the whole plaintext bundle. The
encrypted custody repository keeps the originals.

## Adding An Integration (Routine, No OpenBao Changes)

Access is per namespace subtree, so a new integration for an existing
namespace never touches OpenBao configuration. It is a Git PR plus one scoped
value write:

1. **Git**: add the `ExternalSecret` (and workload wiring) in the consuming
   namespace, reading `guardian/guardian-mgmt/<namespace>/<integration>`.
   CI (`TestOpenBaoSecretScopeConformance`) rejects any remoteRef outside the
   namespace's own subtree, so a beta manifest cannot reference a prod path.
2. **Custody**: restore the custody bundle, append the secret value to
   `/dev/shm/guardian-custody/custody.env`, snapshot with
   `aspect infra custody --action create`, wipe, and extend the
   importer plan (`openbao_secret_import/main.go`) in the same PR — the plan
   is the DR re-seed manifest, so DR keeps working without remembering
   anything out of band.
3. **Value write**: mint a short-lived token for the namespace's
   `secrets-writer` SA and write the value with the official CLI. The OpenBao
   role `guardian-writer-<namespace>` can only write that namespace's
   subtree, so cross-stage mixups are impossible by construction:

   ```sh
   kubectl -n tenant-guardian port-forward svc/guardian-openbao-active 18200:8200 &
   kubectl -n tenant-guardian get secret guardian-openbao-api-tls \
     -o jsonpath='{.data.ca\.crt}' | base64 -d > openbao-ca.crt
   export BAO_ADDR=https://127.0.0.1:18200 BAO_CACERT=$PWD/openbao-ca.crt \
     BAO_TLS_SERVER_NAME=guardian-openbao-active.tenant-guardian.svc
   BAO_TOKEN=$(bao write -field=token auth/kubernetes/login \
     role=guardian-writer-tenant-guardian-beta \
     jwt="$(kubectl -n tenant-guardian-beta create token secrets-writer \
       --audience=openbao --duration=10m)")
   # value via stdin; never on the command line / shell history
   BAO_TOKEN=$BAO_TOKEN bao kv put \
     kv/guardian/guardian-mgmt/tenant-guardian-beta/keycloak/github-oauth \
     GITHUB_CLIENT_SECRET=-
   ```

4. Verify the `ExternalSecret` reports `Ready=True`; force a refresh if
   needed (`kubectl annotate externalsecret ... force-sync=$(date +%s)`).

The `secrets-writer` SAs mount nothing and run nothing; minting their tokens
requires custody kubeconfig access. Steady state keeps zero standing
secret-reading admin: readers are per-namespace ESO roles, writers are
per-namespace short-TTL roles, and nothing can cross subtrees.

## Structural Changes (Re-Initialization)

`initialize` runs only at first initialization and there is no standing admin
credential (the self-init token is revoked, the importer role self-deletes),
so *structural* changes — a new consumer namespace, a new mount or auth
method — ship as a Git edit to the self-init block followed by a state reset.
All KV state is re-importable from custody by design; this is the same path a
cold boot exercises.

1. Land the PR editing the `initialize` block (the namespace's reader+writer
   policy/role pairs + conformance inventory) and the consumer's ESO
   manifests.
2. Wait for Flux to reconcile `guardian-system` so the HelmRelease renders the
   new OpenBao config.
3. Reset raft state (the seal key on the nodes and the audit PVCs stay):

   ```sh
   kubectl -n tenant-guardian scale statefulset guardian-openbao --replicas=0
   kubectl -n tenant-guardian wait pod -l app.kubernetes.io/name=openbao --for=delete --timeout=5m
   kubectl -n tenant-guardian delete pvc data-guardian-openbao-0 data-guardian-openbao-1 data-guardian-openbao-2
   kubectl -n tenant-guardian scale statefulset guardian-openbao --replicas=3
   ```

4. Pod 0 initializes and runs the new self-init block; the others join.
5. Re-run the bootstrap secret import (above) with a fresh env file built from
   custody. Build and run the importer from a checkout at the merged
   revision: the binary embeds the import plan, so a stale checkout silently
   seeds the old plan and the importer's one-shot role is already gone by
   the time the gap is noticed (recover via the namespace's scoped
   `secrets-writer` role, which the new self-init block has just created).
6. Verify every ExternalSecret returns to `Ready=True` and force a refresh if
   needed (`kubectl annotate externalsecret ... force-sync=$(date +%s)`).

ExternalSecrets use `creationPolicy: Orphan` / `deletionPolicy: Retain`, so
already-materialized Secrets keep serving their consumers throughout the
reset window.
