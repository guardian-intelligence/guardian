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
aspect tools install   # provides the pinned kubectl shim used below
bazelisk run //src/infrastructure/cmd/openbao_secret_import:openbao_secret_import -- \
  --kubectl "$(git rev-parse --show-toplevel)/.guardian/tools/bin/kubectl" \
  --kubeconfig "$HOME/.kube/config" \
  --env-file custody.env
```

The importer authenticates through the temporary `guardian-secret-importer`
Kubernetes auth role created by self-init (write access to the whole
`guardian-mgmt` subtree — bootstrap and DR re-seed are the only times a
cross-namespace write credential exists, and the importer deletes its own
role and policy when it finishes). Its import plan — `importPlan` in
`src/infrastructure/cmd/openbao_secret_import/main.go` — is the DR re-seed
manifest: the authoritative list of every secret the system needs after a
raft wipe, keyed by custody env variables. Read the plan in the code, not a
prose mirror of it: its optional Keycloak writes are imported only when the
env file carries that environment's values, and each entry's comment explains
its consumer.

Beyond the kv plan, the importer also owns the `guardian-images` transit
signing key (the image countersigner's key). A reinit recreates the transit
mount empty, and fresh key material would orphan every countersignature
already attached in the registry, so custody is the source of truth: when
`custody.env` carries `openbao_transit_images_signing_key_backup` the key is
restored from that blob verbatim; on first run the importer creates the key
(`exportable` + `allow_plaintext_backup`, required by the backup endpoint and
irreversible) and exports its plaintext backup next to the env file, printing
the custody-append instruction. The exported blob is private key material —
fold it into `custody.env`, snapshot the bundle, and delete the exported
file. Prove the blob restores (see the restore drill in
`docs/openbao-design.md`) before anything relies on the key's signatures.

GitHub App private keys live in custody as PEM files and travel in the env
file base64-encoded (the file is line-oriented): the `guardian-promotions`
key as `github_promotions_app_private_key_b64` and the Postflight Runner key as
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
  "$(base64 -w0 < "$B/keys/postflight-runner.private-key.pem")" >> "$B/import.env"
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
`external-dns`, `company-site`, `guardian-iam`, `guardian-products`,
`guardian-analytics`, `postflight-runner`, `tenant-root`, `tenant-guardian`,
and `tenant-guardian-prod`. The `operator/` subtree is the
exception: custody reference material the importer writes but no standing
role can read. Confirm the live list against the self-init block's
reader/writer pairs (pinned by `TestOpenBaoOperationsInventoryConformance`).

## Adding A Secret (Routine, No OpenBao Changes)

A secret for a namespace that already has a scope never touches OpenBao
config. One PR, then one scoped write:

1. **Git PR** — add the `ExternalSecret` (and workload wiring) in the consuming
   namespace, reading `guardian/guardian-mgmt/<namespace>/<integration>`, and
   extend the importer plan in `openbao_secret_import/main.go` (+ its test) so
   DR re-seeds it. `TestOpenBaoSecretScopeConformance` rejects any remoteRef
   outside the namespace's own subtree — one namespace's manifest cannot
   reference another's path.
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
     role=guardian-writer-tenant-guardian-prod \
     jwt="$(kubectl -n tenant-guardian-prod create token secrets-writer \
       --audience=openbao --duration=10m)")
   BAO_TOKEN=$BAO_TOKEN bao kv put \
     kv/guardian/guardian-mgmt/tenant-guardian-prod/keycloak/github-oauth \
     GITHUB_CLIENT_SECRET=-   # value on stdin
   ```

4. **Verify** — `ExternalSecret` reports `Ready=True`
   (`kubectl annotate externalsecret ... force-sync=$(date +%s)` to refresh
   now).

Crib from these PRs (each is a routine add, no reinit):

- `d34fb5a` — OpenBao-back the Kargo git credential: importer-plan entry +
  ESO wiring for a new secret in an existing namespace.
- `2a44ca6` — Postflight IAM: per-namespace secret resolving into env vars,
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
`guardian-system`, then run the whole reinit as one unattended command from
an up-to-date `origin/main` checkout:

```sh
# 1. build the import env file from custody (never print a value)
aspect infra custody --action restore    # plaintext bundle at /dev/shm/guardian-custody
umask 077
B=/dev/shm/guardian-custody
cp "$B/custody.env" "$B/import.env"
printf 'github_promotions_app_private_key_b64=%s\n' \
  "$(base64 -w0 < "$B/keys/github-promotions-app.private-key.pem")" >> "$B/import.env"
printf 'github_runner_app_prod_private_key_b64=%s\n' \
  "$(base64 -w0 < "$B/keys/postflight-runner.private-key.pem")" >> "$B/import.env"
# The reinit validates the importer's whole plan against import.env BEFORE
# the raft wipe; a custody.env missing a newer required value (e.g.
# zot_countersigner_password) fails fast here — mint it into custody first
# (aspect infra custody --action env-set, value on stdin) and rebuild
# import.env.

# 2. the whole reinit, unattended (consumes and deletes import.env)
aspect infra openbao-reinit

# 3. FIRST RUN OF THE guardian-images TRANSIT KEY ONLY: the importer exported
#    the key's plaintext backup next to import.env and printed the
#    instruction. Append it to custody.env (never echo it), snapshot, and
#    delete the export before the wipe:
#      printf 'openbao_transit_images_signing_key_backup=%s\n' \
#        "$(cat "$B/openbao-transit-guardian-images.backup.b64")" >> "$B/custody.env"
#      aspect infra custody --action create
#      rm "$B/openbao-transit-guardian-images.backup.b64"

# 4. wipe the plaintext bundle the moment the command succeeds
aspect infra custody --action wipe
```

The command executes, in order, and fails fast with a specific message at
each gate:

1. **Preconditions.** The checkout is `origin/main` HEAD (the importer binary
   embeds the import plan; seeding from a stale plan is the reinit's #1
   historical footgun); the `guardian-system` Kustomization is Ready at that
   same revision, so the raft reset boots into the merged self-init block;
   `tenant-guardian` carries `pod-security.kubernetes.io/enforce=privileged`;
   and the import env file exists, is non-empty, and satisfies the importer's
   full plan (`--validate-only`), so a missing custody variable surfaces
   while the raft is still intact. The Pod Security check
   exists because a Cozystack tenant-chart regeneration can SSA-stomp the
   postRenderer that sets the labels
   (`base/app-patches/tenant-guardian-namespace-pod-security.yaml`); with the
   label missing the StatefulSet cannot recreate pods (the hostPath seal
   volume violates baseline PSA) and a partial boot leaves dirty raft state
   (`cluster already has state` crashloop). When the label is missing the
   tool reconciles `guardian-mgmt-app-patches`, then the `tenant-guardian`
   HelmRelease, and proceeds only once the label is back.
2. **Raft reset.** Scale to 0, wait for pod deletion, delete the
   `data-guardian-openbao-{0,1,2}` PVCs (seal keys on nodes and `audit-*`
   PVCs stay), scale back to 3, wait for all members to run ready. A member
   that crashloops on `cluster already has state` is auto-recovered exactly
   once with a fresh pod + data PVC — the proven fix for leftover raft
   debris.
3. **Drill.** The same verification `aspect infra openbao-drill` runs: three
   members, one `cluster_id`, initialized, unsealed, HA-enabled.
4. **Custody re-import.** Runs the importer built from this checkout with
   `--delete-env-file`; the importer removes its own one-shot role and policy
   after verified writes.
5. **Re-relay.** The four in-cluster-generated values the importer does not
   carry, each written through its own scoped `guardian-writer-<ns>` role and
   sourced from its still-materialized `Orphan`/`Retain` Secret (values stay
   in memory, never printed): analytics ClickHouse `ingest` (Secret
   `analytics-ch-ingest`, ns `guardian-analytics`), postflight control-plane
   Postgres `uri` (Secret `postgres-postflight-controlplane-app`, ns
   `tenant-root`), external-dns `CF_API_TOKEN` (Secret
   `cloudflare-external-dns`, ns `external-dns`), and the backups R2 keypair
   (Secret `guardian-backups-creds`, ns `tenant-root`). A missing source
   Secret stops the run and names the upstream source of truth (the
   `guardian-mgmt-cloudflare-tokens` tofu outputs for the Cloudflare-lane
   values); an empty value is never written (`test -s` semantics).
6. **Convergence.** Force-syncs every ClusterSecretStore/SecretStore and
   every ExternalSecret cluster-wide, then polls until all report Ready,
   listing the stragglers on timeout. The store nudge is load-bearing: a
   store that validated while OpenBao was down wedges on a stale
   `InvalidProviderConfig/unable to create client` condition until poked.

Afterwards, the full-system proof:

```sh
aspect infra converged --expected-revision "$(git rev-parse HEAD)"
```

<details>
<summary>Manual steps (DR reference — only when the reinit tool itself is broken)</summary>

```sh
# 0. preconditions — all enforced by the tool, all still required by hand:
#    guardian-system Ready at origin/main HEAD, checkout == origin/main,
#    and the privileged Pod Security label present:
kubectl get ns tenant-guardian \
  -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'
# if not "privileged": annotate reconcile.fluxcd.io/requestedAt on
# Kustomization cozy-fluxcd/guardian-mgmt-app-patches, then on
# HelmRelease tenant-root/tenant-guardian, and re-check before continuing.

# 1. reset raft (seal keys on nodes and audit PVCs stay)
kubectl -n tenant-guardian scale statefulset guardian-openbao --replicas=0
kubectl -n tenant-guardian wait pod -l app.kubernetes.io/name=openbao --for=delete --timeout=5m
kubectl -n tenant-guardian delete pvc data-guardian-openbao-0 data-guardian-openbao-1 data-guardian-openbao-2
kubectl -n tenant-guardian scale statefulset guardian-openbao --replicas=3
# if a member crashloops on "cluster already has state": delete that pod and
# its data-<pod> PVC once and let it boot fresh.

# 2. wait until all three members are up and one raft cluster
aspect infra openbao-drill --kubeconfig=src/infrastructure/talm/kubeconfig

# 3. re-seed from custody — build the importer from MERGED main
#    (Bootstrap Secret Import section has the full custody/env-file recipe)

# 4. re-relay the values the importer does not carry (each a scoped
#    secrets-writer write, value on stdin, `test -s` before every put; the
#    in-cluster-generated ones are sourced from their still-materialized
#    Orphan/Retain Secrets):
#    - analytics ClickHouse ingest password (Secret analytics-ch-ingest key
#      ingest, ns guardian-analytics) → guardian-analytics/clickhouse
#      property ingest
#    - postflight-controlplane Postgres uri (Secret
#      postgres-postflight-controlplane-app key uri, ns tenant-root) →
#      postflight-runner/postgres
#    - external-dns Cloudflare token (Secret cloudflare-external-dns key
#      CF_API_TOKEN, ns external-dns; upstream source when missing:
#      tofu -chdir=src/infrastructure/bootstrap/guardian-mgmt-cloudflare-tokens \
#        output -raw external_dns_token_value) →
#      external-dns/cloudflare property CF_API_TOKEN
#    - backups R2 keypair (Secret guardian-backups-creds, ns tenant-root;
#      upstream outputs r2_backups_access_key_id /
#      r2_backups_secret_access_key) → tenant-root/backups-r2 flat keys
#      accessKey/secretKey/endpoint/bucketName=guardian-backups/region=auto

# 5. nudge every ClusterSecretStore AND force-sync every ExternalSecret
#    (a store validated while OpenBao was down stays wedged on
#    InvalidProviderConfig until annotated):
kubectl annotate clustersecretstore --all force-sync=$(date +%s) --overwrite
kubectl annotate externalsecret --all -A force-sync=$(date +%s) --overwrite
# then confirm every ExternalSecret reports Ready=True.

# 6. verify: the external-dns ExternalSecret Ready=True proves self-init +
#    kv mount + auth roles are live. It reaches Ready only AFTER the
#    external-dns re-relay above.
aspect infra converged --expected-revision "$(git rev-parse HEAD)" \
  --kubeconfig=src/infrastructure/talm/kubeconfig
```

Watch out for:

- **Build the importer from merged `main`, not your branch.** The binary embeds
  the import plan; a stale checkout seeds the OLD plan and its one-shot role
  self-deletes before you notice (recover via the new namespace's scoped
  `secrets-writer` role, which self-init just created).
- **`test -s` the value file before `bao kv put`.** A failed
  `kubectl … | base64 > f` pipeline does not stop `set -euo pipefail`, so an
  empty file silently overwrites a good secret.

</details>
