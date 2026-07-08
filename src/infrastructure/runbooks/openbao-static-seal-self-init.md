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
for an existing namespace never changes this block (see Adding A Secret
below). Only structural changes — a new consumer namespace, a new
mount or auth method — edit the block and re-initialize (see Reinit).

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
If the external-dns ExternalSecret never goes Ready after its value has been
re-relayed (see the re-relay checklist), the cluster likely did not
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

## Which Path? (Read This First)

Almost every secret change is the routine path. Reinit is rare.

- **New secret in a namespace that already has a scope** (a new third-party
  key, a new per-stage value for an existing consumer) → **routine, no
  reinit.** OpenBao config does not change.
- **New consumer namespace, or a new mount/auth method** → **reinit.** This is
  the only trigger. It is not triggered by adding a secret; it is triggered by
  a namespace that has no `guardian-reader-<ns>`/`guardian-writer-<ns>` pair
  yet.

The scoped namespaces that already exist (no reinit needed to write into them):
`external-dns`, `operator`, `company-site`, `guardian-iam`,
`guardian-products`, `guardian-analytics`, `verself-runner`, `tenant-root`,
`tenant-guardian`, and `tenant-guardian-{beta,gamma,prod}`. Confirm the live
list with `TestOpenBaoSecretScopeConformance`'s inventory.

## Adding A Secret (Routine, No OpenBao Changes)

A secret for a namespace that already has a scope never touches OpenBao
config. One PR, then one scoped write:

1. **Git PR** — add the `ExternalSecret` (and workload wiring) in the consuming
   namespace, reading `guardian/guardian-mgmt/<namespace>/<integration>`, and
   extend the importer plan in `openbao_secret_import/main.go` (+ its test) so
   DR re-seeds it. `TestOpenBaoSecretScopeConformance` rejects any remoteRef
   outside the namespace's own subtree — a beta manifest cannot reference a
   prod path.
2. **Custody** — `aspect infra custody --action restore`, append the value to
   `/dev/shm/guardian-custody/custody.env` (never echo it), `--action create`,
   `--action wipe`.
3. **Value write** — mint a 10-minute `secrets-writer` token for that namespace
   and `bao kv put` with the value on stdin (never argv). The
   `guardian-writer-<ns>` role can only write its own subtree, so wrong-stage
   writes fail server-side:

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
   BAO_TOKEN=$BAO_TOKEN bao kv put \
     kv/guardian/guardian-mgmt/tenant-guardian-beta/keycloak/github-oauth \
     GITHUB_CLIENT_SECRET=-   # value on stdin
   ```

4. **Verify** — `ExternalSecret` reports `Ready=True`
   (`kubectl annotate externalsecret ... force-sync=$(date +%s)` to refresh
   now).

Crib from these PRs (each is a routine add, no reinit):

- `d34fb5a` — OpenBao-back the Kargo git credential: importer-plan entry +
  ESO wiring for a new secret in an existing namespace.
- `2a44ca6` — Verself IAM beta: per-stage secret resolving into env vars,
  `substitute: disabled` where `${...}` must survive Flux envsubst.

Watch out for:

- **Value on stdin (`key=-`), never on argv** — argv lands in shell history.
- **Extend the importer plan in the same PR.** Skip it and DR silently loses
  the secret; the plan is the re-seed manifest, kept honest by review.

## Reinit (Structural Changes Only)

Only when adding a **new consumer namespace** or a **new mount/auth method**.
There is no standing admin, so a self-init edit ships with a raft reset. KV
state is re-importable from custody by design — this is the cold-boot path.
Consumers stay up throughout: ExternalSecrets are `Orphan`/`Retain`.

Land the PR editing the `initialize` block (new reader+writer pair +
conformance inventory + consumer ESO manifests), wait for Flux to reconcile
`guardian-system`, then:

```sh
# 1. reset raft (seal keys on nodes and audit PVCs stay)
kubectl -n tenant-guardian scale statefulset guardian-openbao --replicas=0
kubectl -n tenant-guardian wait pod -l app.kubernetes.io/name=openbao --for=delete --timeout=5m
kubectl -n tenant-guardian delete pvc data-guardian-openbao-0 data-guardian-openbao-1 data-guardian-openbao-2
kubectl -n tenant-guardian scale statefulset guardian-openbao --replicas=3

# 2. wait until all three members are up and one raft cluster
aspect infra openbao-drill --kubeconfig=src/infrastructure/talm/kubeconfig

# 3. re-seed from custody — build the importer from MERGED main (see below)
#    (Bootstrap Secret Import section has the full custody/env-file recipe)

# 4. verify: the external-dns ExternalSecret Ready=True proves self-init +
#    kv mount + auth roles are live. It reaches Ready only AFTER the
#    external-dns re-relay in the checklist below — run that first, then
#    read this signal.
aspect infra converged --expected-revision "$(git rev-parse HEAD)" \
  --kubeconfig=src/infrastructure/talm/kubeconfig
```

Then re-relay the values the importer does not carry (each a scoped
`secrets-writer` write, value on stdin; the in-cluster-generated ones are
sourced from their still-materialized Secrets):

- analytics ClickHouse ingest password → `guardian-analytics/clickhouse`
  property `ingest`
- verself-controlplane Postgres `uri` → `verself-runner/postgres`
- external-dns Cloudflare token →
  `kv/guardian/guardian-mgmt/external-dns/cloudflare` property `CF_API_TOKEN`,
  sourced from `tofu -chdir=src/infrastructure/bootstrap/guardian-mgmt-cloudflare-tokens output -raw external_dns_token_value`,
  written via the `guardian-writer-external-dns` scoped role
- backups R2 keypair → `kv/guardian/guardian-mgmt/tenant-root/backups-r2`
  (flat keys `accessKey`/`secretKey`/`endpoint`/`bucketName=guardian-backups`/`region=auto`),
  `accessKey` from output `r2_backups_access_key_id`, `secretKey` from output
  `r2_backups_secret_access_key`, written via `guardian-writer-tenant-root`

Force-sync every ExternalSecret and confirm `Ready=True`.

Watch out for:

- **Build the importer from merged `main`, not your branch.** The binary embeds
  the import plan; a stale checkout seeds the OLD plan and its one-shot role
  self-deletes before you notice (recover via the new namespace's scoped
  `secrets-writer` role, which self-init just created).
- **`test -s` the value file before `bao kv put`.** A failed
  `kubectl … | base64 > f` pipeline does not stop `set -euo pipefail`, so an
  empty file silently overwrites a good secret.
