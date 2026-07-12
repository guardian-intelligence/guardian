# Registry design: the in-cluster OCI tier

Status: active as of 2026-07-11 (slices R1 zot tier + mirror flip, and R2
countersigner write path).
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
  while everything the registry serves is safe for anonymous eyes. **Trigger
  condition, not a date: the moment any confidential artifact would land in
  zot, authenticated node pulls (Talos per-registry auth in machine config)
  must already be in place.** Countersignatures do not trip this:
  they are zot-local (not on ghcr) but public by nature — verifiable
  statements whose whole purpose is to be read against a public key.
- **In-cluster writers.** Exactly one: the countersigner's `countersigner`
  htpasswd user, granted create/update under `guardian-intelligence/**`
  (where its signature referrers land) while `**` stays anonymous
  read-only. htpasswd rather than bearer SA-token federation is a verified
  constraint, not a shortcut: in the pinned zot, enabling ANY `http.auth.bearer`
  config makes the bearer handler the exclusive authn middleware with no
  anonymous fallthrough — it would break anonymous node pull-through
  (confirmed in v2.1.18 source; the OIDC bearer support added in v2.1.14 is
  real but all-or-nothing). If node pulls ever authenticate (the ring above
  escalates), bearer SA-token federation becomes the natural replacement for
  the htpasswd user. The credential is custody-held
  (`zot_countersigner_password`); the importer derives zot's bcrypt htpasswd
  line from it, and ESO projects both halves into `zot-countersigner-auth`.
- **Humans.** Platform-admin `kubectl` (port-forward/exec) is the R1
  break-glass surface. A Keycloak OpenID client (cozy realm, PR-able via
  the EDP operator) becomes worth wiring only when a human-facing surface
  (UI/API ingress) exists.
- **The internet: nothing.** GitHub runners never push to zot; they publish
  to ghcr and zot ingests. The registry has no ingress and no public
  exposure, which deletes that attack surface rather than authenticating
  it.

## The countersigner (R2)

`zot-countersigner.yaml`: a level-triggered assurance loop over the
first-party image digests the cluster's workload specs declare. Each run,
per digest, addressed through the mirror (`10.8.0.201:5000/...@digest`): if
a countersignature already verifies against the transit key's public half,
done; otherwise verify the digest's Fulcio signature against its canonical
per-workflow identity (pinned Sigstore trusted root, fully offline — zot
proxies the signature material from ghcr) and only then sign with
`openbao://guardian-images` and re-verify. Countersignatures attach as OCI
1.1 referrers, never legacy `.sig` tags — a tag GET re-triggers on-demand
sync, which would clobber a locally-modified tag, while sync never touches
local referrers. Everything the loop talks to is in-cluster (OpenBao, zot,
apiserver, vminsert); its only entities-scoped allowance is TCP/5000 to the
zot VIP for non-socket-LB datapaths. The write grant itself carries a tag
condition: signature referrers are by-digest pushes and digest content is
self-addressing, so a leaked countersigner credential cannot repoint any
tag nodes pull.

Loudness: `GuardianCountersignerUnsignedImages` pages when the unsigned
count fails to touch zero across 45 minutes (Fulcio verification failing,
an unmapped first-party repo, transit signing unavailable, or the write
path broken), and `GuardianCountersignerSilent` pages on metric absence.
A new first-party image is unsigned until its identity is added to the
script's map — the page is the onboarding reminder.

The signing key's custody model (importer-owned restore-or-create, backup
blob in custody.env, restore drill before reliance, never rotate casually)
lives in `docs/openbao-design.md`.

## Where this goes (the inversion)

End state: zot is the operational origin; ghcr.io demotes to one publish
marketplace among npm/PyPI/crates. Until in-cluster builders exist, GitHub
publishers keep pushing ghcr first — that is the only place Fulcio
identities are mintable, so ghcr-as-origin is currently correct, not debt.
The inversion is gated on: in-cluster CI capacity, a passed re-cache drill
(wipe the PVC, watch on-demand sync repopulate under the canary — the drill
must also confirm countersignature referrers are re-mintable afterwards,
since a wiped PVC loses them until the countersigner's next runs re-sign),
and the countersigner's write path proven in production. Outbound publication
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
