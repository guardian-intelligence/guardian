# Repository structure

The Cozystack-native tree separates paid capacity, physical host facts,
cluster bootstrap, and runtime desired state. Do not encode all of those in a
single bootstrap file.

## Top-level `src` layout

```text
src/
  fleet/opentofu/       provider-owned infrastructure state
  hosts/                physical bare-metal asset facts and Talos inputs
  clusters/             Kubernetes control-plane and bootstrap intent
  environments/         Crossplane/Flux environment configuration bags
  crossplane/           reusable platform APIs and compositions
  k8s/                  bootstrap-only and platform Kubernetes manifests
  guardian/             host lifecycle CLI
  schemas/              Guardian-owned CUE schemas
  tools/                repo-pinned tool inputs
```

## Ownership boundaries

`src/fleet/opentofu/` owns provider-side infrastructure objects: Latitude
servers, provider tags, billing/plan/site/project facts, SSH keys, and
`allow_reinstall`. It does not wipe hosts or deploy Talos.

`src/hosts/<asset>/host.cue` owns the durable physical facts for one bare-metal
asset: provider server ID, IP/gateway, NIC MAC, disk serials, storage policy,
Talos schematic, and Talos patches. Asset IDs survive wipes, renames, and
environment reassignments.

`src/clusters/<cluster>/cluster.cue` owns the Kubernetes control-plane boundary:
membership, network ranges, Talos/Kubernetes/Cozystack versions, and bootstrap
safety policy. Guardian currently has two intended clusters:

- `guardian-nonprod` for dev and gamma
- `guardian-prod` for prod

`src/environments/<environment>/environment.cue` owns the post-bootstrap
environment bag consumed by Crossplane/Flux. Dev and gamma intentionally share
the nonprod cluster unless they earn a harder API-server boundary.

`guardian up` owns host come-up only: verify the selected host, wipe/reimage as
explicitly allowed, bootstrap Talos/Kubernetes/Cozystack substrate, then hand
off to reconcilers. It must not become an OpenTofu wrapper or a generic
deployment runner.

The first OpenTofu stack is deliberately flat:

```text
src/fleet/opentofu/latitude/
  versions.tf
  providers.tf
  hosts.tf
  imports.tf
  outputs.tf
```

It imports the existing Latitude `ash-bm-001` server instead of creating a new
server. Remote encrypted state with locking is the next hardening step before
team or prod writes.
