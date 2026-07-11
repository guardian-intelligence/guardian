# Registry design: the in-cluster OCI tier

Status: active as of 2026-07-11 (PR for task: zot registry tier, slice R1).
Complements `supply-chain-design.md` (trust model, signing) and
`adrs/0005-no-in-cluster-object-storage.md` (why the registry stores on a
PVC, not an object store).

## Role and invariant

zot (`deployments/guardian/system/zot-helmrelease.yaml`, namespace
`tenant-guardian`, VIP `10.8.0.201:5000`) is the in-cluster OCI registry
tier. Its governing invariant: **the registry is a rebuildable cache, never
the only copy of anything**. Git holds the pins, ghcr.io holds the
artifacts, custody holds the keys; the registry's PVC is not backed up, and
losing it costs one re-sync. Every future responsibility this tier takes on
(countersignature storage, origin-first pushes) must preserve that property
or explicitly renegotiate it here first.

Today it is a pull-through mirror of ghcr.io. Node containerd reaches it
through the MetalLB VIP once the Talos mirror flip lands (separate PR to
`talm`); manifests keep their `ghcr.io/...` names, so the union lock, the
provenance VAP, Flux, and Kargo are all unaware the mirror exists. On any
mirror miss or outage containerd falls back to upstream ghcr implicitly —
behavior the mirror-flip PR must verify on a live node before the fleet
depends on it. The mirror must never be listed alongside the upstream
endpoint (an explicit upstream entry disables the implicit fallback and
deadlocks pulls on a mirror 404), and `skipFallback` remains exclusive to
the dark-bundle lane.

Two config settings are load-bearing and must never be relaxed:
`preserveDigest: true` and `http.compat: ["docker2s2"]`. Without them zot
converts docker-media-type manifests to OCI on sync, which changes digests
— fatal for a fully digest-pinned estate and for every cosign signature
served through the mirror.

## Fallback is redundancy we can hear, not silence

A dead mirror with working fallback produces zero workload symptoms by
construction. Three mechanisms make the state observable:

- the mirror canary (`zot-mirror-canary.yaml`) pulls a pinned manifest
  through the VIP every 10 minutes and requires the exact expected digest —
  pages on failure and on absence;
- `ZotRegistryDown` pages when the metrics scrape target drops, precisely
  because nothing else will;
- zot's Prometheus metrics feed the steady-state assertion that the mirror
  serves the pull path (hit-rate visibility once the mirror flip lands).

Fallback-to-ghcr is the availability posture, not a hidden crutch: it stays
configured permanently as the DR path for the registry tier itself. Paging
covers the mirror being down, the mirror serving wrong bytes, and — now
that the mirror flip makes misses an observable steady-state signal — the
mirror failing to fulfill misses (`ZotMirrorSyncBroken`: sustained 404/5xx
on the pull path while cache misses fall back silently to upstream).

## Auth: rings and their trigger conditions

- **Nodes (read).** Anonymous read, network-scoped (VIP on the node VLAN,
  ingress admitted only from host/remote-node identities). This holds only
  while the registry's content is a strict subset of what is publicly
  served by ghcr. **Trigger condition, not a date: the moment any
  non-public artifact would land in zot, authenticated node pulls (Talos
  per-registry auth in machine config) must already be in place.**
- **In-cluster writers.** None exist in R1, so no write path exists in R1:
  `accessControl` grants anonymous read only, and without a matching policy
  every push is 401. The first writer — the countersigner (R2) — arrives
  with zot's bearer ServiceAccount-token federation (`http.auth.bearer.oidc`
  against the cluster issuer) and per-repo write policies, tested against
  its real consumer. No credentials are pre-provisioned.
- **Humans.** Platform-admin `kubectl` (port-forward/exec) is the R1
  break-glass surface. A Keycloak OpenID client (cozy realm, PR-able via
  the EDP operator) becomes worth wiring only when a human-facing surface
  (UI/API ingress) exists.
- **The internet: nothing.** GitHub runners never push to zot; they publish
  to ghcr and zot ingests. The registry has no ingress and no public
  exposure, which deletes that attack surface rather than authenticating
  it.

## Where this goes (the inversion)

End state: zot is the operational origin; ghcr.io demotes to one publish
marketplace among npm/PyPI/crates. Until in-cluster builders exist, GitHub
publishers keep pushing ghcr first — that is the only place Fulcio
identities are mintable, so ghcr-as-origin is currently correct, not debt.
The inversion is gated on: in-cluster CI capacity, a passed re-cache drill
(wipe the PVC, watch on-demand sync repopulate under the canary), and the
countersigner proving the ServiceAccount write path. Outbound publication
from zot to marketplaces must use signature-carrying copies (`cosign copy`
/ regsync) — plain image copies silently drop cosign referrers.

GC note for future writers: the estate pulls by digest, so mirrored
manifests are stored untagged — and zot's default when GC is on and no
retention policy is configured is `deleteUntagged: true` (verified against
v2.1.18 source and reproduced live: a by-digest-synced manifest was deleted
on the first GC pass under a bare config). The shipped retention block
pinning `deleteUntagged: false` is therefore load-bearing; removing it
deletes the cache. `deleteReferrers` must also stay false so
countersignatures survive their subjects' churn.
