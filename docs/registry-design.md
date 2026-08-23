# Registry design: the in-cluster OCI tier

Status: active as of 2026-08-23 (slice R1 zot tier + mirror flip; next: the
inversion, gated on in-cluster CI).
Complements `supply-chain-design.md` (trust model, signing).

## Role and invariant

zot (`deployments/guardian/system/zot-helmrelease.yaml`, namespace
`tenant-guardian`, VIP `10.8.0.201:5000`) is the in-cluster OCI registry
tier. Its governing invariant: **the registry is a rebuildable cache, never
the only copy of anything**. Git holds the pins, ghcr.io holds the
artifacts; the registry's PVC is not backed up, and
losing it costs one re-sync. Every future responsibility this tier takes on
(origin-first pushes) must preserve that property
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

The on-demand `syncTimeout: 2m` is also load-bearing for fallback. zot's
default is three hours and a disconnected request continues in the background;
without the bound, the node's five-minute pull context expires while zot still
owns the singleflight, so containerd never gets an opportunity to fall back to
ghcr. Two minutes leaves a normal cache miss time to copy while preserving a
meaningful upstream-fallback window.

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
  while everything the registry serves is safe for anonymous eyes. **Trigger
  condition, not a date: the moment any confidential artifact would land in
  zot, authenticated node pulls (Talos per-registry auth in machine config)
  must already be in place.**
- **In-cluster writers.** None. The mirror is anonymous read-only
  everywhere (`accessControl` is the write lock: without it zot serves
  anonymous read-WRITE). If a writer ever becomes necessary, note that in
  the pinned zot enabling ANY `http.auth.bearer` config makes the bearer
  handler the exclusive authn middleware with no anonymous fallthrough — it
  would break anonymous node pull-through (confirmed in v2.1.18 source; the
  OIDC bearer support added in v2.1.14 is real but all-or-nothing) — so an
  htpasswd user is the compatible shape while node pulls stay anonymous.
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

The released set is the release manifest
(`deployments/guardian/system/release-manifest.yaml`), the reviewable
definition of what Guardian releases — the postflight CLI's release
channels: promotions bump a lane in the same commit as the channel pin (the
CLI nightly Kargo stage carries the extra `yaml-update`;
`TestReleaseManifestCoversReleaseChannels` holds hand-made bumps to the
same rule). Marketplaces keep their CI-platform OIDC provenance (npm, PyPI
keyless publishes); the OCI lane's trust statement is the Fulcio keyless
signature CI mints at build time (`supply-chain-design.md`).

The inversion itself is gated on: in-cluster CI capacity, a passed re-cache
drill (wipe the PVC, watch on-demand sync repopulate under the canary), and
renegotiating the rebuildable-cache invariant above, which stops holding
the moment zot is the only copy of anything.

GC note for future writers: the estate pulls by digest, so mirrored
manifests are stored untagged — and zot's default when GC is on and no
retention policy is configured is `deleteUntagged: true` (verified against
v2.1.18 source and reproduced live: a by-digest-synced manifest was deleted
on the first GC pass under a bare config). The shipped retention block
pinning `deleteUntagged: false` is therefore load-bearing; removing it
deletes the cache. `deleteReferrers` must also stay false so
referrer artifacts survive their subjects' churn.
