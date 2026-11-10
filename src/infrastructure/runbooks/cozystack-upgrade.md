# Cozystack platform upgrade

The cozy-installer pin is a substrate doorbell (`docs/dependency-management.md`): the pin PR starts this runbook and merging it is the runbook's last step, so Git never claims a version the cluster does not run. Flux never applies the installer; `aspect infra upgrade-cozystack` does, from a checkout of the pin PR's branch.

Research against the exact upstream tag being installed: `docs/changelogs/vX.Y.Z.md` in cozystack/cozystack, especially "Breaking Changes and Required Actions".

## 1. Prerequisites PR (merges first)

Anything the new platform needs from Guardian-owned objects lands on main before the installer moves, because Flux keeps re-applying main throughout the upgrade window:

- **Admission inventory.** Operator-spawned images that land in the image-provenance namespaces (`base/admission/image-provenance.yaml`) must already be in `images.declared.lock`, or every pod the upgrade restarts there is denied. CloudNativePG is the recurring case: its operator image is the bootstrap init container of every Postgres pod in `tenant-guardian-prod`, by tag, so a CNPG minor bump needs `ghcr.io/cloudnative-pg/cloudnative-pg:<new>@sha256:<index digest>` declared.
- **Guardian-owned copies of platform objects.** Where Guardian applies an object Cozystack also writes (the kube-ovn Subnets in `base/networking/subnet-mtu.yaml`), any field an upstream hook edits must already carry the hook's result, or Flux reverts it within one interval.

## 2. Pre-flight (read persona)

```sh
aspect infra auth --persona=read
kubectl get hr -A | grep -v True          # start from a converged platform
kubectl get nodes                          # no DiskPressure; upgrades pull many images
kubectl get dv -A                          # no CDI import in flight
tools/ops/cluster-watch --status
```

## 3. Upgrade (write-all persona)

From a checkout of the pin PR's branch:

```sh
aspect infra auth --persona=write-all
aspect infra upgrade-cozystack --wait_timeout=20m
```

The installer upgrades the operator; the operator then reconciles the platform Package to the packages artifact the installer names, running the platform migrations as a pre-upgrade hook. Watch it converge:

```sh
kubectl -n cozy-system logs job/cozystack-migration-hook   # read promptly; warnings live here
kubectl get packages.cozystack.io -A
kubectl get hr -A -w
```

## 4. Verify, then merge the pin PR

- Installer Helm revision and chart digest: `helm -n cozy-system history cozystack`.
- Platform artifact: the `cozystack-packages` digest the operator reconciled must match the declared lock.
- Live image audit: add any new operator-spawned image the cluster now runs to `images.declared.lock` in the pin PR (the image-provenance policy names undeclared images in covered namespaces; elsewhere, diff `kubectl get pods -A -o jsonpath='{..image}'` against the lock).
- `aspect infra watch --mode=convergence` quiet, Alerta quiet for ~15 minutes.

Then merge the pin PR. It moves `COZYSTACK_INSTALLER_VERSION` in `.aspect/tasks/infra.axl`, the cozy-installer and cozystack-packages entries in `images.declared.lock`, and the version named in `docs/cluster-architecture.md` and `runbooks/cold-boot-bootstrap.md`.

## Upgrade notes by release

### 1.6.3 → 1.7.0

- Migrations 54–60 run. Guardian runs none of the apps they adopt (Redis, tenant Kubernetes, SeaweedFS, HTTPCache, FoundationDB, KubeVirt VMs).
- CloudNativePG 1.28 → 1.30.0 restarts every Postgres instance once. Prerequisite: `cloudnative-pg:1.30.0` declared (index digest `sha256:a2701eb9…`).
- Cilium 1.19 → 1.20 moves from `ipam.mode kubernetes` to a cluster pool in the last /21 of `networking.podCIDR` (10.244.248.0/21 here) through a post-upgrade hook that also restarts every agent twice. The hook writes the pool, and each node's current Cilium Ingress IP, into `ovn-default` `spec.excludeIps`. Prerequisite: both already in `subnet-mtu.yaml`. Read the current Ingress IPs before upgrading: `kubectl get ciliumnodes -o custom-columns=NODE:.metadata.name,INGRESS:.spec.ingress.ipv4`.
- Multus drops its CPU limit upstream (#4830), which supersedes `app-patches/cozy-multus-cpu-resources.yaml`.
- vmalert now notifies every Alertmanager replica upstream, which is what `app-patches/monitoring-vmalert-notifiers.yaml` does; the patch can retire once the cluster runs 1.7.0.
- Talos stays on v1.13 for this upgrade. Cozystack 1.7 targets Talos v1.14.2, which needs `drbd_transport_tcp` in `machine.kernel.modules` before any node moves; that is the separate Talos runbook.
