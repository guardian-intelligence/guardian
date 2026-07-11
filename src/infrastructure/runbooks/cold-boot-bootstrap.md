# guardian-mgmt Cold-Boot Bootstrap

From wiped bare metal to a fully converged guardian-mgmt cluster. Every step
below was executed and verified live in the 2026-07-01/02 all-three-node
cold-boot drill; deviations the drill uncovered are called out inline. The
trust model is the cold-bootstrap constraint in AGENTS.md: the local checkout,
its Bazel-built artifacts, and the operator custody bundle are everything a
from-nothing bring-up may require.

## Inputs (custody bundle)

1. **Static seal key** — 32 raw bytes, held offline. Fresh cluster ⇒ mint a
   fresh key (all durable OpenBao state must be reimportable or exported per
   the Transit custody rules in `docs/openbao-design.md`):

   ```sh
   bazelisk run //src/infrastructure/cmd/openbao_static_seal_key:openbao_static_seal_key -- \
     --cluster guardian-mgmt --region ash --out-dir <custody-dir>/static-seal
   ```

   Declare the printed fingerprint in
   `src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml`
   (init-container key path + sha check, `current_key_id`, `current_key`) and
   in `openBaoStaticSealKeyID` in
   `src/infrastructure/tests/openbao_conformance_test.go`. Never print or
   commit the key bytes.
2. **`custody.env`** — the 8 operator keys (Cloudflare tokens, R2
   credentials, account id) matching the `openbao_secret_import` schema. Keep
   it OUTSIDE the repo: `aspect infra validate` (run inside `bootstrap`)
   refuses an in-repo env file, and the importer deletes the file on success —
   keep a custody backup.
3. **Latitude API token** and the workstation SSH key registered on the
   account (`ssh_W9EKa3oBbaRoB` = the guardian-controller key).

Workstation needs: this checkout, `bazelisk`, `podman`, the `aspect` CLI, and
a public IP the nodes can reach. All other binaries are repo-pinned: the
downloaded ones (talm, talosctl, kubectl, helm, oras, flux, boot-to-talos)
materialize under `$(bazelisk info output_base)/external/…`, and hauler is
Bazel-built from source (`bazelisk build //src/tools/hauler:hauler`).

## Offline bundle (dark-uplink input)

`aspect infra bundle` builds the artifact half of a dark-uplink cold boot
into a fresh `dist/bundle/`:

- `images.lock` — the GENERATED union inventory (declared lock + every
  digest-pinned image ref rendered from the manifest trees), derived by
  `//src/infrastructure/cmd/imageset`. A pure function of the checkout, so
  the operator can re-derive it offline and byte-compare.
- `hauler-manifest.yaml` — the union lock projected into a
  `content.hauler.cattle.io/v1` Images manifest (Tier-1 lock tests gate the
  build, so the haul is provably complete relative to what the repo renders).
- `store/` + `haul.tar.zst` — every locked artifact (container images, OCI
  Helm charts, Flux OCI artifacts) pulled digest-exact into a Hauler content
  store and saved as one portable archive. Tagged lock refs (the Talos system
  images) are stored under both their digest and their tag so the served
  mirror answers Talos's tag-addressed pulls (`skipFallback` makes an
  unserved tag a fatal 404).
- `bundle-manifest.yaml` — the git revision plus sha256 digests of the lock
  and the haul.
- `images.lock.sigbundle` — CI's keyless cosign signature over the exact
  union lock this haul was synced from, fetched from
  `ghcr.io/guardian-intelligence/supply-chain:images.lock-<union-sha256>`
  (published by the `images-lock-sign` workflow on main, which derives the
  identical union from the same revision). The bundle build fails if the
  union was never signed — an unsigned inventory is an unreviewed one. It also verifies the signature and runs `bundle --verify`
  end-to-end, so a drive that finished building has already passed the
  exact checks the operator repeats offline at bring-up.

docker.io anonymously rate-limits pulls (~10/hour), so a full sync
practically requires `bazelisk run //src/tools/hauler -- login docker.io`
credentials or several `aspect infra bundle --resume` windows; resume
re-fetches only the refs the store is missing.

The complete dark drive is: the `dist/bundle/` output (`haul.tar.zst`,
`bundle-manifest.yaml`, `images.lock`, `images.lock.sigbundle`,
`hauler-manifest.yaml`) as `<drive>/bundle/`, the source-built hauler,
bundle, and imageset binaries
(`bazelisk build //src/tools/hauler:hauler //src/infrastructure/cmd/bundle:bundle //src/infrastructure/cmd/imageset:imageset`
— copy the bundle and imageset binaries to the drive as `bundle-bin` and
`imageset-bin` so they cannot be confused with the `bundle/` directory),
the pinned flux CLI binary
(`bazelisk build @multitool//tools/flux`, then copy
`$(bazelisk info execution_root)/$(bazelisk cquery --output=files @multitool//tools/flux)`
— like every fetched tool it exists only where Bazel has run with network),
the pinned cosign binary (same recipe with `@multitool//tools/cosign`),
this repo checkout at the same revision (which carries the pinned Sigstore
trusted root at `src/infrastructure/bootstrap/bundle/sigstore-trusted-root.json`
— refresh it when refreshing the drive; Sigstore rotates it on the order of
months), and the custody bundle above.

Dark mode is entered and exited via PRs plus four bring-up steps:

0. **Verify the drive** (mirror host, offline — no network needed; identities
   are recorded in `docs/supply-chain-design.md`). Run from the repo checkout
   root, with `<drive>` the mounted drive and both binaries the prebuilt
   copies the drive carries (do not `bazelisk run` here: `bazel run`
   executes in the runfiles directory, so the repo-relative paths below
   would not resolve):

   ```sh
   <drive>/imageset-bin \
     --declared src/infrastructure/bootstrap/bundle/images.declared.lock \
     --repo-root "$(pwd)" --out /tmp/images.lock.derived
   cmp /tmp/images.lock.derived <drive>/bundle/images.lock

   <drive>/cosign verify-blob --bundle <drive>/bundle/images.lock.sigbundle \
     --certificate-identity "https://github.com/guardian-intelligence/guardian/.github/workflows/images-lock-sign.yml@refs/heads/main" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     --trusted-root src/infrastructure/bootstrap/bundle/sigstore-trusted-root.json \
     <drive>/bundle/images.lock

   <drive>/bundle-bin --verify \
     --bundle-dir <drive>/bundle \
     --images-lock <drive>/bundle/images.lock \
     --revision "$(git rev-parse HEAD)"
   ```

   The first pair proves the drive's union lock is exactly what this
   checkout derives (declared + rendered); the cosign command proves that
   union was signed from reviewed main history; the last proves the haul
   and hauler-manifest on the drive are hash-bound to that same union.
   The re-derivation runs the drive-carried binaries, so the
   drive-to-checkout binding holds under the custody model — the drive
   travels with the operator, like the seal key. Only then load the store.

1. **Pre-drill PR**: flip `darkBundleMirror.enabled: true` in
   `src/infrastructure/talm/values.yaml` and regenerate the node configs —
   every locked upstream registry then mirrors to the haul with
   `skipFallback: true` (a miss fails loudly instead of silently dialing
   the internet; our hauler build serves repo paths verbatim, matching
   containerd's mirror dialect), and node NTP points at the mirror host.
2. **Serve + source push** (mirror host, before applying machine configs):

   ```sh
   hauler store load --filename=haul.tar.zst --store=store
   hauler store serve registry --store=store --readonly=false &
   flux push artifact oci://148.113.198.223:5000/guardian/source:dark \
     --path . --source "$(git remote get-url origin)" \
     --revision "$(git rev-parse HEAD)"
   ```

   The writable serve is what lets the repo checkout become the cluster's
   Flux source with no Git server; a serve restart re-copies the store into
   the backend without deleting the pushed artifact, but re-push after any
   backend wipe.
3. **Dark GitOps entrypoint**: `kubectl apply -k
   src/infrastructure/bootstrap/sync-dark` — sync.yaml with every
   Kustomization's source resolved to the `guardian-oci` OCIRepository, plus
   the `guardian-source` ConfigMap that keeps Flux's own re-application of
   sync.yaml resolving the same way. `aspect infra converged
   --expected-revision <sha>` gates as in steady state — pass the same git
   sha you gave `flux push artifact --revision`; the proof matches it against
   the Kustomization's origin revision (an OCIRepository's applied revision
   is `<tag>@<manifest-digest>`, which does not contain the git sha).

Exiting dark is ordered and gated, not fire-and-forget:

1. Merge the aftermath PR flipping `darkBundleMirror.enabled` back off and
   regenerate the node configs (restores public registry mirrors + NTP).
2. `kubectl apply -k src/infrastructure/bootstrap/sync-steady` — restores the
   `guardian-source` ConfigMap to GitRepository values and patches the
   top-level Kustomizations back. Do NOT just delete the ConfigMap: Flux
   skips substitution on an empty variable map, and the literal placeholders
   then fail the CRD enum.
3. Force the flip through both generations rather than waiting on the 10m
   intervals: `flux reconcile kustomization guardian-mgmt-base`, then once it
   is Ready `flux reconcile kustomization guardian-system` (guardian-mgmt-dns-controller
   re-sources from Git only after guardian-system re-applies it). Confirm every
   Kustomization's `sourceRef.kind` reads `GitRepository` and
   `aspect infra converged --expected-revision <steady-sha>` passes.
4. Only then delete the `guardian-oci` OCIRepository — deleting it while any
   Kustomization still sources from it strands that slice.

## Custody replication

The custody bundle is secret-zero and replicating it is an operator
obligation that cannot be automated: the seal key may never touch
Kubernetes, Git, CI, R2, or any OpenBao-backed path (and transit exports are
offline-custody only), so no system the cluster controls can ever hold a
complete copy of the bundle. Cloud-KMS unseal or Shamir recovery shares
would only relocate the root of trust, not remove it.

`aspect infra custody` is the mechanism (see `runbooks/custody.md`): the
encrypted restic repository it maintains is the bundle's only at-rest form,
and the offline copies below are copies of that repository, verified in
place with `--action verify --read-data --repo <mounted-copy>`.

- Keep at least two copies of the load-bearing set — the seal key plus its
  metadata, the `custody.env` backup, and any `transit/backup` exports —
  on encrypted offline media in two physical locations, neither of them the
  datacenter hosting the cluster.
- Never co-locate a copy with raft snapshots or OpenBao ciphertext. Transit
  exports are offline-custody only: never in R2, and never on the same
  medium as the R2 credentials that can fetch snapshots — key material plus
  ciphertext in one place is full OpenBao compromise.
- Refresh every copy on each custody event (seal-key rotation, operator-key
  change, durable Transit key creation) and record copy locations and
  last-refresh dates — never contents — in the custody directory's README.
- Loss math: custody lost while the cluster lives is recoverable (re-copy
  the seal key from a key-bearing node, re-export the operator keys from
  OpenBao KV, rebuild the bundle immediately). Custody and cluster lost
  together forfeits OpenBao contents (accepted: no recovery keys) and forces
  operator-credential reissue through each provider's console. Once the
  first durable Transit consumer exists, its exported keyring is replaceable
  nowhere — replication must be in place before that key ships.

## Pre-flight

1. **Insurance capture** (if the old cluster still runs): copy the current
   seal key (verify sha256 == filename fingerprint), a `custody.env` backup,
   and optionally a raft filesystem tar to custody. Every image the cluster
   pulls — including the guardian-built `company-site` — lives on public
   registries (`company-site` on ghcr.io, pushed and cosign-signed by CI on
   every merge that changes it), so no registry contents need capturing and a
   steady-uplink cold boot needs no bootstrap mirror. If a pinned digest has
   vanished upstream, recovery is a rebuild through main: re-run the last
   main-push run of `company-site-image` (or merge a trivial content
   change), CI rebuilds, pushes, and cosign-signs a fresh digest, then a
   pin-only PR
   promotes it — the provenance gate only accepts digests signed by the
   canonical CI identity, so a workstation-built digest cannot be pinned
   (see docs/supply-chain-design.md, "Promotion: how digests move").
2. **Pre-drill PR on main**: fresh seal fingerprint, any repins, and
   `images.declared.lock` current. Flux converges from main — nothing lands on
   the cluster that is not merged.

## Reimage (Latitude)

Fire per node (announce each; IPs and server-level VLAN assignments survive):

```sh
curl -X POST "https://api.latitude.sh/servers/<server-id>/reinstall" \
  -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
  -d '{"data":{"type":"reinstalls","attributes":{"operating_system":"ubuntu_24_04_x64_lts","hostname":"<node>","ssh_keys":["ssh_W9EKa3oBbaRoB"]}}}'
```

Server IDs: ash-earth `sv_vAPXaMxKM5epz`, ash-water `sv_8mop5gZo8Njxv`,
ash-wind `sv_nPRbajqEB5koM`.

**The outcome is bimodal — poll BOTH ssh:22 and Talos:50000 and branch:**

- **Ubuntu came up** (ssh answers as `ubuntu@`): identify disks BY SERIAL
  (`/dev/nvmeXnY` enumeration is unstable across boots; the drill saw Ubuntu
  land on the former Talos disk). Wipe the non-root disk after a
  root-disk/serial refusal guard (`blkdiscard -f` + `wipefs -a`) — stale
  DRBD/LINSTOR or etcd state must not survive. Then scp the pinned
  boot-to-talos and kexec into maintenance mode:

  ```sh
  ssh ubuntu@<ip> 'sudo /tmp/boot-to-talos -yes -mode boot \
    -image ghcr.io/cozystack/cozystack/talos:v1.13.0@sha256:c2c092ad…37044'
  ```

  The SSH session hangs when kexec fires — run under a timeout and verify with
  `talosctl get disks --insecure -n <ip> -e <ip>` instead. Boot mode writes
  nothing to disk; the node keeps its static public IP.
- **Talos maintenance mode already** (`talosctl get disks --insecure`
  answers, port 22 closed): Latitude wiped the disks and left the node
  netbooted in a recent Talos. Verify `get discoveredvolumes` shows no
  partitions; a same-minor maintenance runtime applies our config fine and the
  digest-pinned installer image is what lands on disk. Skip straight to apply.

## Talos + talm

All from `src/infrastructure/talm/` with the pinned binaries.

1. **Secret-zero** (talm has no `gen secrets`; `init` would clobber the real
   chart — run it in a scratch dir):

   ```sh
   talm init --root <scratch> --name guardian-mgmt --preset cozystack \
     --cluster-endpoint https://10.8.0.250:6443 --talos-version v1.13.0 --force
   ```

   Copy `secrets.yaml`, `talm.key`, `talosconfig`, `secrets.encrypted.yaml`,
   `talosconfig.encrypted` into `src/infrastructure/talm/` (all gitignored;
   the aspect tasks refuse to run without them). Point the client at the
   public IPs: `talosctl config endpoint <ip1> <ip2> <ip3>`.
2. **Apply per node, overlay stacked, validation skipped** (pre-flight rejects
   the Layer2VIPConfig link `enp1s0f0.2140`, which only exists after this same
   apply's VLANConfig lands):

   ```sh
   talm apply -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml -i --skip-resource-validation
   ```

   Maintenance apply reports "Applied configuration without a reboot" — the
   install+reboot follows automatically; wait for the authenticated API
   (`talm`/`talosctl version` with the talosconfig).
3. **Bootstrap etcd exactly once, on whichever node is up first**:
   `talm bootstrap -f nodes/<node>.yaml`. The VIP activates on
   `enp1s0f0.2140`; peers join as learners and promote.
4. **Kubeconfig** (off-VLAN workstation cannot reach the VIP):

   ```sh
   aspect infra kubeconfig --install
   ```

   The task fetches the kubeconfig through a public Talos endpoint, verifies
   that the rendered Kubernetes server is one of the declared guardian-mgmt API
   endpoints, backs up any existing `~/.kube/config`, and installs the refreshed
   `admin@guardian-mgmt` context. Keep an off-VLAN copy OUTSIDE the repo for
   `--kubeconfig` flags (public IPs are in the cert SANs).

## Seal-key placement

No repo tool exists; the working pattern is two-phase (a one-shot
`kubectl debug node/… -i` dies at stdin EOF mid-script):

1. `kubectl -n kube-system debug node/<node> --profile=sysadmin
   --image=quay.io/openbao/openbao@sha256:436eaf…7c115 -- sleep 900`
2. Stream the key into a synchronous exec (key bytes travel only over the
   API-server exec channel; only the fingerprint is ever printed), then delete
   the pod:

   ```sh
   kubectl -n kube-system exec -i <debugger-pod> -- sh -ec '
     d=/host/var/lib/guardian/openbao/static-seal
     mkdir -p "$d"
     cat > "$d"/unseal-<id>.key.tmp
     chown root:1000 "$d" "$d"/unseal-<id>.key.tmp
     chmod 0750 "$d"
     chmod 0440 "$d"/unseal-<id>.key.tmp
     mv "$d"/unseal-<id>.key.tmp "$d"/unseal-<id>.key
     got=$(sha256sum "$d"/unseal-<id>.key | cut -d" " -f1)
     size=$(wc -c < "$d"/unseal-<id>.key | tr -d " ")
     [ "$got" = "<id>" ] && [ "$size" = 32 ] && echo "PLACED-OK" || { echo "PLACE-FAILED"; exit 1; }
   ' < <custody-dir>/static-seal/unseal-<id>.key
   ```

Target state on every key-bearing node (all three today):
`/var/lib/guardian/openbao/static-seal/unseal-<id>.key`, dir `0750 root:1000`,
key `0440 root:1000`. Works on NotReady nodes (nodeName bypasses the
scheduler).

## Declarative handoff

```sh
# env from the custody custody.env: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
# (cloudflare_r2_* keys), AWS_ENDPOINT_URL_S3, AWS_EC2_METADATA_DISABLED=true
aspect infra bootstrap --kubeconfig <off-vlan-kubeconfig-copy> \
  --endpoints <node-public-ip> --nodes "<ip1>,<ip2>,<ip3>"
```

The endpoint/node overrides are REQUIRED off-VLAN (defaults dial the VIP's
Talos API). This runs tofu against the R2 backend, the full validate suite,
kubeconfig refresh, the Talos L2 gate, and the Cozystack 1.5.0 installer.
If the installer's pre-install hook times out, check for scheduling blockers
first — the drill found a latent all-nodes NoSchedule taint this way. If a
helm release fails terminally, `helm uninstall` it before rerunning
(`upgrade --install` refuses a failed revision-1 release).

Then the one-time GitOps apply and convergence. The bootstrap entrypoint is
an overlay, not sync.yaml itself: kubectl cannot resolve the
`${GUARDIAN_SOURCE_*}` placeholders that let Flux flip between the GitHub
GitRepository and a dark-mirror OCIRepository source.

```sh
kubectl apply -k src/infrastructure/bootstrap/sync-steady
# dark uplink: kubectl apply -k src/infrastructure/bootstrap/sync-dark
# (after serving the haul and pushing the repo artifact — see below)
```

Flux is a single `flux` Deployment (5 containers) in cozy-fluxcd. Convergence
order: platform/platform-patches → storage (LINSTOR claims the blank data
disks) → base (tenant-root, core services) → guardian-system (OpenBao, whose
self-init creates the KV/Transit/auth config) → dns-controller/company-prod.
Expected transients that self-heal:

- pods ContainerCreating until the kubeovn HelmRelease installs the CNI
  plugin binary (the conflist chains kube-ovn before cilium-cni);
- the OpenBao StatefulSet rejected by PodSecurity until the tenant-guardian
  HelmRelease install completes and the app-patches postRenderer labels the
  namespace `enforce=privileged` (the StatefulSet controller retries).

When Cozystack convergence stalls, diagnose with `kubectl get hr -A` (the
dependency chain bottoms out at `cozy-cilium/cilium`) and pod sandbox events;
helm-controller stops retrying once retries exhaust — a values change or a
`reconcile.fluxcd.io/requestedAt` annotation re-triggers.

## Import + proof

```sh
bazelisk run //src/infrastructure/cmd/openbao_secret_import:openbao_secret_import -- \
  --kubectl "$(pwd)/.guardian/tools/bin/kubectl" \  # from: aspect tools install
  --kubeconfig <off-vlan-kubeconfig-copy> \
  --env-file /dev/shm/guardian-custody/custody.env  # after: aspect infra custody --action restore

aspect infra converged --expected-revision "$(git rev-parse HEAD)" \
  --kubeconfig <off-vlan-kubeconfig-copy>

aspect infra openbao-drill \
  --kubeconfig <off-vlan-kubeconfig-copy>
```

The importer writes the three KV paths with readback verification, removes its
temporary role/policy, and deletes the env file. The converged proof requires
every declared Kustomization Ready at the expected revision; certificates,
the OpenBao StatefulSet, and ESO stores gate Kustomization
readiness through Flux health checks declared in the manifests. The status
drill verifies one raft `cluster_id` across unsealed members.

**DR gates** (definitions in `docs/openbao-residue-inventory.md`): the
cold-start gate falls out of the steps above plus an ESO-synced consumer
(ExternalDNS reporting "All records are already up to date" against
Cloudflare) and a Transit encrypt/decrypt roundtrip. For the stateful gate,
create a temporary drill policy/auth-role pair imperatively with `bao`
(self-init owns steady-state OpenBao config and runs only at first
initialization; hand-applied OpenBao config is outside Flux and must be
cleaned up by hand), then: sentinel key
`exportable=true allow_plaintext_backup=true` →
encrypt → `transit/backup` export → restore into a throwaway
`bao server -dev` container → decrypt the cluster's ciphertext. Shred the test
keyring and delete the drill policy/role and sentinel afterwards.

## Analytics ClickHouse (guardian-analytics)

The analytics/observability ClickHouse (`deployments/analytics/system`) is a
raw Altinity CHI + CHK on `local-retain` volumes with 3-way
ReplicatedMergeTree, so a single node loss re-syncs from a surviving replica
(proven live: a 60×20k-batch kill test with one passive and one active replica
deleted mid-ingest lost zero acknowledged rows). Cold-boot notes:

- The CHI/CHK/collector images (`clickhouse-server`, `clickhouse-keeper`,
  `otel-collector-contrib`) are digest-pinned in the manifests and declared
  lock, so the dark bundle carries them; the namespace comes up from Git
  with no custody input.
- The stored data is **not** in any custody bundle by design: it is
  25-month-TTL business analytics + 6-month OTel traces, reconstructable from
  the ongoing event stream, not from-nothing-critical state. A full-cluster
  wipe loses history but not the pipeline — the schema re-applies from the
  idempotent DDL Job and ingestion resumes on the next request. A periodic
  `clickhouse-backup`→S3 sidecar (as tenant-root's CHI has) is the tracked
  follow-up if analytics history ever becomes recovery-critical.

## Aftermath

1. `aspect infra edge-health` (expect all targets green) and, with
   `CLOUDFLARE_API_TOKEN` set from the dns-lb-provisioner key,
   `aspect infra tofu-init --root guardian-mgmt-dns` followed by
   `bazelisk run @multitool//tools/tofu:workspace_root -- -chdir=src/infrastructure/bootstrap/guardian-mgmt-dns plan -input=false -var-file=src/infrastructure/bootstrap/backend.tfvars`
   (expect zero infrastructure changes — node IPs are unchanged). DNS records
   themselves are owned by the in-cluster `external-dns` controller, not this
   root; this plan only covers the Cloudflare Load Balancer pool/monitor
   objects, which is why it stays raw OpenTofu rather than an `aspect`
   subcommand. Zone edge policy (AOP, the cache ruleset, bot management) is
   the separate `guardian-mgmt-edge-policy` root, applied with the
   edge-policy-provisioner token — verify its plan is empty too when that
   token is at hand.
2. **Audit the declared inventory against the live cluster**: compare
   running pod imageIDs in the covered namespaces against
   `src/infrastructure/bootstrap/bundle/images.declared.lock` and PR only
   the additions the drill newly spawned (operator images no manifest
   renders; kubelet/etcd from `talosctl image ls --namespace system`). This
   is an additive audit, not a scrape-replace: the disjointness invariant
   rejects entries the manifests render, the guardian-image-provenance
   admission policy flags undeclared images as they are created, and the
   vap-denial-canary pages if that enforcement ever goes silent. Prune
   declared entries only with live evidence that nothing schedules them
   (mind intermittent CronJob images).
3. **Verify the datapath MTU pair on every node**: `ovn0` must match the
   Subnet MTU (1362), not kube-ovn's iface−100 default (1320). The Subnet
   specs only govern pod interfaces; ovn0 comes from the kube-ovn-cni
   `--mtu` flag, declared as `kube-ovn.mtu` on the `cozystack.networking`
   Package (platform-patches). kube-ovn-cni re-enforces its computed value,
   so a manual `ip link set` does not stick. A mismatch black-holes full-MSS
   external traffic whenever Cilium load-balances an externalIP flow to the
   node-local backend: the kernel forward path emits "frag needed" ICMPs
   referencing the post-DNAT pod IP, which clients cannot associate — ~1/3
   of TLS handshakes and large downloads stall while health checks (small
   payloads) stay green. Found live 2026-07-02 serving guardianintelligence.org.
   Probe with repeated ≥100KB fetches against each origin IP, not `/healthz`.

## Why this runbook exists

## Data restore (the platform converging is not the end)

Everything above yields a healthy cluster with EMPTY databases. Disaster
recovery is finished only when the stateful stores are back from R2:

1. `backup-audit.md` is the entrypoint: freshness checks against the
   `guardian-backups` bucket, then the restore drills. Its siblings carry
   the mechanics — `postgres-backup-restore.md` (CNPG/barman PITR for
   `tenant-root/postflight-controlplane` and `tenant-guardian-prod/keycloak`)
   and `analytics-clickhouse.md` (clickhouse-backup, plus the chart bugs
   that bite during restore).
2. Post-restore re-relays: the analytics ingest password must be relayed
   again after any guardian-analytics re-seed (`analytics-clickhouse.md`),
   and Keycloak realm state rides in its Postgres — verify logins after the
   PITR, not just pod health.
3. Declare recovery complete only when `aspect infra converged` passes AND
   a query returns real pre-disaster data from each restored store.

The 2026-07-01 drill rebuilt the cluster from Git + custody alone and caught
eight classes of latent state that only a cold boot exposes: a Git-pinned
image Harbor had garbage-collected, an all-nodes register-with-taints
NoSchedule taint that never went live on registered nodes, pod-network
components enabled inside the CNI's own helm release, a controller image
predating its CRD schema, an operator installed out-of-band and never
declared, a workload pointed at a hand-era namespace, cross-tenant
isolation admitting the old topology by accident, and a declared pod MTU
whose node-side half (ovn0) was never declared and defaulted wrong
(surfaced only under real full-MSS traffic the day after). Treat "the live
cluster works" as evidence of nothing; only Git + custody + this procedure
count.
