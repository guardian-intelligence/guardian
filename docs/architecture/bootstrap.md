# From-zero bootstrap

A site converges from a wiped machine to a healthy control plane through
three layers, each owned by an existing, battle-tested supervisor. There is
no custom resident daemon: `guardian` is a controller-side CLI that injects
inputs and watches convergence.

## Layers

**Layer 0 — host.** Talos Linux. The OS is an immutable A/B image produced by
the Talos Image Factory from a checked-in schematic
(`src/sites/<site>/talos/schematic.yaml`). The schematic ID plus the Talos
version is a content-addressed description of the entire host. `machined`
supervises the kubelet; the kubelet supervises the control plane via static
pods; the control plane supervises everything above it. Host configuration is
one declarative machine config applied over the Talos API
(`talosctl apply-config`), generated from per-site patches under
`src/sites/<site>/talos/patches/`.

**Layer 1 — secrets.** OpenBao with raft integrated storage, deployed as a
StatefulSet from the OCI image built at
`//src/infrastructure-components/openbao:image`. Initialization, unseal, and
restore are reconciled by an operator (bank-vaults is the reference shape);
the founder-held site root key is injected at bootstrap and never stored in
the cluster.

**Layer 2 — everything else.** GitOps reconciliation from OCI artifacts with
cosign verification at pull time. Not yet present in this repo.

## guardian CLI

`guardian` owns the controller-side, human-initiated steps. Each site is one
checked-in `site.yaml` (`src/sites/<site>/site.yaml`) naming the node, its
static addressing facts, and the Talos schematic and patches. The bootstrap surface
is two verbs plus operator config: run
`guardian config site src/sites/dev/site.yaml` once (the path is stored
absolute in `${XDG_CONFIG_HOME:-~/.config}/guardian/config.yaml`), then a
drill is `guardian down --yes && guardian up`. Both verbs also accept an
explicit `<site.yaml>` positional argument, which overrides the configured
site. `guardian config` with no arguments prints the config file path and
contents. Run both verbs from the repo root: only the configured site path
is stored absolute; the paths inside `site.yaml` (schematic, patches) and
the component manifests are repo-root relative.

1. `guardian down --yes [site.yaml]` takes the node to Talos maintenance
   mode with a wiped system disk, from whichever state it is in. A node
   running configured Talos is reset over its authenticated API
   (`talosctl reset`, non-graceful because a single-node etcd cannot leave
   its own cluster). A node running any other Linux is kexec'd into the
   Talos maintenance image over SSH: the node downloads the factory's boot
   assets directly (its datacenter route to the factory is the one that
   matters), guardian appends static `ip=` addressing from `site.yaml` to
   the pinned metal command line (sites have no DHCP), loads with
   kexec-tools, and `systemctl kexec`.
   SSH authentication is the caller's ambient setup; guardian never holds
   credentials. No provider API is involved; provisioning compute is outside
   guardian's scope. The `--yes` acknowledgement is required because this
   destroys everything on the node.
2. `guardian up [site.yaml]` converges from runtime truth. It generates or
   reuses the site's Talos secrets bundle (under
   `${XDG_STATE_HOME:-~/.local/state}/guardian/<cluster>/`, never in the
   repo), regenerates machine config from the pinned installer image and
   checked-in patches, then probes the node: a configured node gets the
   config re-applied; a maintenance-mode node gets its disk inventory
   verified against both `node.installDiskSerial` and `node.zfsDiskSerial`,
   the first install, and a one-time etcd bootstrap. The install disk is
   selected by serial through a generated `machine.install.diskSelector`
   patch, so the reserved ZFS disk is never an install target even when two
   identical NVMes re-enumerate. Both paths end with the seed registry up,
   workspace
   artifacts pushed into it, components applied, and rollouts awaited. A
   sealed or uninitialized OpenBao counts as converged; init, restore, and
   unseal are operator actions.

Version pins consumed by the CLI are compile-time constants, and `talosctl`
and `kubectl` ride in the binary's runfiles; changing what the fleet runs is
a reviewed commit, and nothing is taken from the operator's PATH.

## Image transport: the seed registry

Workload images travel controller to node without any external registry or
registry credential. The workspace is the root of trust; a registry is
transport for content-addressed bytes the build already produced.

- `up` applies the seed-registry Deployment
  (`src/infrastructure-components/seed-registry/k8s/seed-registry.yaml`):
  CNCF distribution pinned by digest, storage on the node's `/var`, a fixed
  ClusterIP, and no exposure outside the cluster.
- Pushes happen through a kubectl port-forward: guardian reads each
  component's Bazel-built OCI layout from its runfiles and pushes it by
  digest with go-containerregistry. No docker daemon, no credentials beyond
  the Talos PKI.
- Pulls happen through a Talos registry mirror
  (`src/sites/<site>/talos/patches/registry-mirror.yaml`): manifests
  reference `registry.guardian.internal/<component>@<built digest>`, a
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

- ZFS pool creation and zvol churn. The `siderolabs/zfs` system extension is
  in the dev schematic and the second NVMe (`node.zfsDiskSerial`) is reserved
  from the installer, so the kernel module and an empty data disk are present;
  creating the pool and driving it under vm-orchestrator churn is unbuilt.
- KubeSpan versus Cilium WireGuard encryption for WAN worker nodes; run one,
  never both.
- TLS for the OpenBao listener: serving-cert issuance is part of the cluster
  tracer. The checked-in dev config sets `tls_disable = true` and must not
  leave the dev site.
