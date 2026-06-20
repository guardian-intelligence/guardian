# From-zero bootstrap

A host converges from a wiped machine to a healthy control plane through
three layers, each owned by an existing, battle-tested supervisor. There is
no custom resident daemon: `guardian` is a controller-side CLI that injects
inputs and watches convergence.

## Layers

**Layer 0 — host.** Talos Linux. The OS is an immutable A/B image produced by
the Talos Image Factory from a checked-in schematic
(`src/hosts/<host>/talos/schematic.yaml`). The schematic ID plus the Talos
version is a content-addressed description of the entire host. `machined`
supervises the kubelet; the kubelet supervises the control plane via static
pods; the control plane supervises everything above it. Host configuration is
one declarative machine config applied over the Talos API
(`talosctl apply-config`), generated from per-host patches under
`src/hosts/<host>/talos/patches/`.

**Layer 1 — secrets.** Talos owns Kubernetes Secret encryption-at-rest via
the generated `cluster.secretboxEncryptionSecret`; Guardian must not add a
separate kube-apiserver `EncryptionConfiguration` path unless Talos drops
that contract. OpenBao with raft integrated storage is the durable secret
authority above Kubernetes, deployed as a StatefulSet via the official OpenBao
Helm chart (upstream chart + image in dev; vendored and digest-pinned for prod
per the release contract). External Secrets
Operator projects the small set of workload Kubernetes Secrets from OpenBao;
workloads never get broad Secret read permissions, and the projected
Kubernetes Secrets stay under Talos-owned encryption-at-rest. Fresh Bao
initialization is performed by `guardian up`, which converges a wiped node to a
running, auto-unsealed OpenBao. The seal is the one per-environment axis: dev
auto-unseals from a non-secret *static toy key* so an agent can bring it up
unattended and run the daily wipe-restore DR drill with no human, while prod
auto-unseals via a dedicated *transit* OpenBao (self-hosted KMS, no cloud
dependency) with TLS and HA. `bao operator init` yields recovery keys plus a
root token — low-value in dev; in prod the recovery keys are the break-glass,
held in the operator's password manager (2FA-gated) and never on the cluster.
Restored Bao reuses the values in the snapshot; missing restored paths are
explicit schema migrations. Raft snapshots travel to R2 as ciphertext only.

**Layer 2 — everything else.** GitOps reconciliation from
signed, digest-addressed artifacts. The CLI may install or seed this
substrate, but it must not become the runtime deployment engine.

## guardian CLI

`guardian` owns the controller-side, human-initiated steps. Each host has a
checked-in `host.json` (`src/hosts/<host>/host.json`) naming the target node
and its cluster/environment assignment.
Post-Kubernetes desired state lives separately in
`src/environments/<environment>/environment.yaml` as an
`EnvironmentConfig` plus any environment XR instances. The active bootstrap
surface is `guardian up -f src/hosts/<asset>/host.json`; plan mode is the
default and destructive execution requires `--execute`.

1. `guardian down --yes [host.json]` takes the node to Talos maintenance
   mode with a wiped system disk, from whichever state it is in. A node
   running configured Talos is reset over its authenticated API
   (`talosctl reset`, non-graceful because a single-node etcd cannot leave
   its own cluster). A node running any other Linux is kexec'd into the
   Talos maintenance image over SSH: the node downloads the factory's boot
   assets directly (its datacenter route to the factory is the one that
   matters), then boots into Talos maintenance with boot-to-talos.
   SSH authentication is the caller's ambient setup; guardian never holds
   credentials. No provider API is involved; provisioning compute is outside
   guardian's scope. The `--yes` acknowledgement is required because this
   destroys everything on the node.
2. `guardian up -f [host.json]` converges host and bootstrap substrate from
   runtime truth. It generates or
   reuses the cluster's Talos secrets bundle (under
   `${XDG_STATE_HOME:-~/.local/state}/guardian/<cluster>/`, never in the
   repo), including Talos's `secretboxEncryptionSecret` for Kubernetes
   Secret encryption-at-rest. It boots stock Ubuntu into Talos maintenance with
   boot-to-talos, renders machine config with Talm's live discovery, applies
   that config, and runs the one-time etcd bootstrap. Storage device ownership
   is not modeled in Guardian's host come-up path; Cozystack/LINSTOR owns
   post-Kubernetes storage discovery and policy. Both paths end with the
   seed registry up, bootstrap artifacts pushed into it, and the secrets
   substrate converged first. OpenBao applies and becomes reachable; Bao is
   restored or fresh-initialized/unsealed; `kv/` and Kubernetes auth are
   configured; Flux and External Secrets Operator are installed or made
   reachable. From there the
   cluster reconcilers own environment desired state. `guardian up` may seed the
   initial reconciler inputs and wait for required readiness, but it does not
   choose product versions, evaluate SLO policy, promote channels, or own
   runtime manifests. Blocking projections must have ready ExternalSecrets and
   Kubernetes Secrets before their consumers are treated as converged.
   Nonblocking projections, such as early Directus authoring secrets, keep the
   desired state visible and warn on missing Bao values without blocking public
   serving convergence. With auto-unseal the pod is unsealed as soon as it
   reaches its seal provider, so the projection gate only waits for readiness —
   there is no manual unseal step in normal operation. Recovery keys from init
   are break-glass for root-token regeneration and seal migration, held
   off-cluster in the operator's password manager.

Version pins consumed by the CLI are compile-time constants, and `talosctl`
and `kubectl` ride in the binary's runfiles; changing what the fleet runs is
a reviewed commit, and nothing is taken from the operator's PATH.

## Image transport: the seed registry

Bootstrap images travel controller to node without any external registry or
registry credential. During local drills the workspace is the root of trust; in
the release path, signed channel artifacts are the root of trust. The seed
registry is transport for content-addressed bytes the build already produced.

- `up` applies the seed-registry Deployment
  (`src/k8s/bootstrap/seed-registry/base`):
  CNCF distribution pinned by digest, storage on the node's `/var`, a fixed
  ClusterIP, and no exposure outside the cluster.
- Pushes happen through a kubectl port-forward: guardian reads required
  Bazel-built OCI layouts from its runfiles and pushes them by digest with
  go-containerregistry. No docker daemon, no credentials beyond the Talos PKI.
- Pulls happen through a Talos registry mirror
  (`src/hosts/<host>/talos/patches/registry-mirror.yaml`): manifests
  reference `registry.guardian.internal/<artifact>@<built digest>`, a
  virtual name containerd resolves to the seed registry's ClusterIP from the
  host netns. Manifests never name a real registry, so the transport can
  change (node-local, in-cluster, hosted) without touching workloads.

External roots of trust that remain: the Talos Image Factory, the public
registries Talos itself pulls system images from, and the one digest-pinned
seed-registry image. Removing those is the air-gapped bundle milestone
(Talos image cache plus self-built imager output), which changes transport,
not contracts.

## Disaster recovery contract

The backup writer and the restore reader are deliberately asymmetric.

- **Backup (write path) knows about R2.** A scheduled job takes a raft
  snapshot and uploads it with a digest manifest. Fully automated; a missed
  backup is an alert.
- **Restore (read path) takes a blob and a digest.** The restore component's
  contract is `(blob ref, sha256)`. It is deterministic, testable offline
  against a local file, and carries no provider coupling. Fetching the blob
  from R2 is glue in `guardian`.
- **The decision to restore is never automated.** A node that wrongly decides
  it has been wiped and auto-restores a stale snapshot over live state is a
  silent catastrophe. Restore is one idempotent command invoked by an
  operator or agent, drilled by repeatedly wiping the dev box.

Secret-zero is irreducible and stays small: the founder-held site root key
and a read-only R2 credential for the backup bucket. Both are injected at
bootstrap and wiped from the host after unseal.

## Open questions for the tracer

- Dynamic local storage provisioning and zvol churn. The first implemented
  storage slice creates/imports one ZFS product-workload pool and exposes it
  through static local PVs. A CSI driver or Guardian
  storage controller can replace the static PV bridge once workload churn
  needs dynamic claims.
- KubeSpan versus Cilium WireGuard encryption for WAN worker nodes; run one,
  never both.
- TLS for the OpenBao listener: serving-cert issuance is part of the cluster
  tracer. The checked-in dev config sets `tls_disable = true` and must not
  leave the dev site.
