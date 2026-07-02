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
2. **`DELETE_ME.env`** — the 8 operator keys (Cloudflare tokens, R2
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

- `hauler-manifest.yaml` — `images.lock` projected into a
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

docker.io anonymously rate-limits pulls (~10/hour), so a full sync
practically requires `bazelisk run //src/tools/hauler -- login docker.io`
credentials or several `aspect infra bundle --resume` windows; resume
re-fetches only the refs the store is missing.

The complete dark drive is: `haul.tar.zst`, the source-built hauler binary
(`bazelisk build //src/tools/hauler:hauler`), the pinned flux CLI binary
(from `$(bazelisk info output_base)/external/+http_archive+flux_linux_amd64/`
— like every fetched tool it exists only where Bazel has run with network),
this repo checkout at the same revision, and the custody bundle above.

Dark mode is entered and exited via PRs plus three bring-up steps:

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
   is Ready `flux reconcile kustomization guardian-openbao-ops` (its three
   children re-source from Git only after it re-applies them). Confirm every
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

- Keep at least two copies of the load-bearing set — the seal key plus its
  metadata, the `DELETE_ME.env` backup, and any `transit/backup` exports —
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
   seal key (verify sha256 == filename fingerprint), a `DELETE_ME.env` backup,
   and optionally a raft filesystem tar to custody. Copy any Harbor-only
   images out — **do not trust that Git-pinned digests still exist in
   Harbor**: the drill found the pinned company-site manifest garbage-collected
   (pods ran from containerd cache only). Recovery is rebuild-from-source
   (`bazelisk build //src/products/company/site:image`,
   `//src/services/secrets/openbao/operator:image`) and repin.
2. **Bootstrap mirror**: `podman run -d --name guardian-bootstrap-mirror
   -p 5000:5000 -v <dir>:/var/lib/registry docker.io/library/registry:2`,
   firewalled to the three node public IPs. Seed the Harbor-hosted images (digests pinned in
   `src/infrastructure/bootstrap/bundle/images.lock`) in scoped layout (repo path prefixed with `harbor.guardianintelligence.org/`)
   using the pinned oras, e.g.
   `oras cp --recursive --to-plain-http --from-oci-layout
   bazel-bin/…/image@sha256:<digest> 127.0.0.1:5000/harbor.guardianintelligence.org/guardian/<name>:edge`.
   The talm chart's `bootstrapHarborMirror` value renders a
   RegistryMirrorConfig with `overridePath: true` pointing at this registry;
   containerd falls back to the real Harbor once it exists, so the mirror is
   inert in steady state. Verify reachability from a cluster pod (node SNAT
   uses the public IPs).
3. **Pre-drill PR on main**: fresh seal fingerprint, mirror endpoint, any
   repins, `images.lock` current (`//src/infrastructure/tests:talm_render_test`
   enforces rendered-refs ⊆ lock). Flux converges from main — nothing lands on
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
   talm kubeconfig --nodes <public-ip> --endpoints <public-ip> --merge=false --force
   kubectl --kubeconfig kubeconfig config set-cluster guardian-mgmt --server=https://<public-ip>:6443
   ```

   Keep an off-VLAN copy OUTSIDE the repo for `--kubeconfig` flags (public IPs
   are in the cert SANs).

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
# env from the custody DELETE_ME.env: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
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
disks) → base (tenant-root, core services) → guardian-system (OpenBao) →
openbao-ops → dns-controller/company-prod. Expected transients that self-heal:

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
  --kubectl "$(bazelisk info output_base)/external/+http_file+kubectl_linux_amd64/file/kubectl" \
  --kubeconfig <off-vlan-kubeconfig-copy> \
  --env-file <custody>/DELETE_ME.env

aspect infra converged --expected-revision "$(git rev-parse HEAD)" \
  --kubeconfig <off-vlan-kubeconfig-copy>

aspect infra openbao-drill \
  --kubeconfig <off-vlan-kubeconfig-copy>
```

The importer writes the three KV paths with readback verification, removes its
temporary role/policy, and deletes the env file. The converged proof requires
every declared Kustomization Ready at the expected revision; certificates,
the OpenBao StatefulSet, operation CRs, and ESO stores gate Kustomization
readiness through Flux health checks declared in the manifests. The status
drill verifies one raft `cluster_id` across unsealed members.

**DR gates** (definitions in `docs/openbao-residue-inventory.md`): the
cold-start gate falls out of the steps above plus an ESO-synced consumer
(ExternalDNS reporting "All records are already up to date" against
Cloudflare) and a Transit encrypt/decrypt roundtrip. For the stateful gate,
create a temporary drill policy/auth-role pair as OpenBao operation CRs
(hand-applied CRs are not pruned — Flux prune only removes inventory-labeled
objects), then: sentinel key `exportable=true allow_plaintext_backup=true` →
encrypt → `transit/backup` export → restore into a throwaway
`bao server -dev` container → decrypt the cluster's ciphertext. Shred the test
keyring and delete the drill CRs and sentinel afterwards.

## Aftermath

1. **Repopulate Harbor** so the workstation mirror stops being load-bearing.
   Pushes through the public edge fail for real blobs (Cloudflare limits;
   direct-to-origin dies mid-blob) — use an in-cluster one-shot skopeo pod
   (`quay.io/skopeo/stable@sha256:94f5c5e26997e2e78c234ec9abf19a391c234b39eb22e6d1210d0b527c97dcc8`,
   skopeo v1.16.1) with a hostAlias mapping
   `harbor.guardianintelligence.org` to the root-ingress-controller ClusterIP,
   login via stdin, `skopeo copy --all --preserve-digests` from the mirror.
   Create the `guardian` project with `metadata.public="true"` and verify
   anonymous pulls at the pinned digests through the edge.
2. `aspect infra edge-health` (expect all targets green) and
   `aspect infra dns-apply --mode plan` with `CLOUDFLARE_API_TOKEN` set from
   the dns-lb-provisioner key (expect zero infrastructure changes — node IPs
   are unchanged).
3. **Re-scrape `src/infrastructure/bootstrap/bundle/images.lock`** from the live cluster (workload section from
   pod imageIDs; kubelet/etcd from `talosctl image ls --namespace system`) and
   PR it.
4. Retire the mirror whenever convenient; the machine-config mirror entry is
   inert once Harbor serves the digests.
5. **Verify the datapath MTU pair on every node**: `ovn0` must match the
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
