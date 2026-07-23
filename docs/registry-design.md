# Registry design: the in-cluster OCI tier

Status: active as of 2026-07-12 (slices R1 zot tier + mirror flip, R2
countersigner write path, and R3 release projector; next: the inversion,
gated on in-cluster CI).
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
  the htpasswd user. The credential (`zot_countersigner_password`) and its
  derived bcrypt htpasswd line live in OpenBao, and ESO projects both halves
  into `zot-countersigner-auth`.
- **Humans.** Platform-admin `kubectl` (port-forward/exec) is the R1
  break-glass surface. A Keycloak OpenID client (cozy realm, PR-able via
  the EDP operator) becomes worth wiring only when a human-facing surface
  (UI/API ingress) exists.
- **The internet: nothing.** GitHub runners never push to zot; they publish
  to ghcr and zot ingests. The registry has no ingress and no public
  exposure, which deletes that attack surface rather than authenticating
  it.

## The countersigner (R2)

`zot-countersigner.yaml`: a level-triggered assurance loop that mints
Guardian's release signature. Its estate is the release manifest
(`release-manifest.yaml`) — the same reviewable definition of the released
set the release projector enforces against, mounted into both loops, so
what gets signed and what gets projected cannot drift; the two gauges
(`guardian_countersigner_first_party_images`,
`guardian_projector_release_images`) agreeing is the standing witness. Each
run, per digest, addressed through the mirror (`10.8.0.201:5000/...@digest`): if
a countersignature already verifies against the transit key's public half —
transparency-log inclusion included — done; otherwise verify the digest's
Fulcio signature against its canonical per-workflow identity (pinned
Sigstore trusted root, fully offline — zot proxies the signature material
from ghcr) and only then sign with `openbao://guardian-images`, uploading
to the Rekor transparency log, and re-verify. Every signing event is
publicly logged; the bundle embeds the inclusion proof, so verifying it —
log check included — needs no network, only the public key and the same
pinned trusted root. Countersignatures attach as OCI 1.1 referrers, never
legacy `.sig` tags — a tag GET re-triggers on-demand sync, which would
clobber a locally-modified tag. The mirror disables implicit legacy-tag
sync during referrer discovery: zot otherwise enumerates and redundantly
copies the CI `.sig` manifest before returning its local OCI referrers.
The explicit Fulcio verification still resolves that exact CI signature tag
on demand when a digest needs countersigning.
The loop's one internet path is the Rekor upload (world:443 — FQDN
allowlisting needs the L7 DNS proxy the chained datapath rules out);
everything else it talks to is in-cluster (OpenBao, zot, apiserver,
vminsert), and a failed upload fails the sign loudly rather than minting
an unlogged signature. In dark operation signing therefore pauses while
verification is unaffected — consistent with the scope ruling that the
dark bundle carries no signing requirement. The write grant itself carries
a tag condition: signature referrers are by-digest pushes and digest
content is self-addressing, so a leaked countersigner credential cannot
repoint any tag nodes pull.

Loudness: `GuardianCountersignerUnsignedImages` pages when the unsigned
count fails to touch zero across 45 minutes (Fulcio verification failing,
an unmapped first-party repo, transit signing unavailable, the Rekor
upload failing, or the write path broken), and
`GuardianCountersignerSilent` pages on metric absence.
A new first-party image is unsigned until its identity is added to the
script's map — the page is the onboarding reminder.

The signature's scope is deliberately bounded: it exists for what Guardian
*releases*, not for what runs. There is no requirement that a pod run a
Guardian-signed image — what runs is governed by the merge gate and the
provenance VAP — and no admission-time signature verification (a verifying
webhook would put a new SPOF in the pod-create path for a guarantee the
release boundary already carries).

The signing key's lifecycle (created in Transit, recovered as data through
raft-snapshot DR, never rotated casually) lives in `docs/openbao-design.md`.

## Where this goes (the inversion)

End state: zot is the operational origin; ghcr.io demotes to one publish
marketplace among npm/PyPI/crates. Until in-cluster builders exist, GitHub
publishers keep pushing ghcr first — that is the only place Fulcio
identities are mintable, so ghcr-as-origin is currently correct, not debt.

The publication boundary carries the invariant: **nothing Guardian releases
to a public marketplace ships without a verified Guardian countersignature.**
The release projector holds it — a CronJob beside the countersigner
(`deployments/guardian/system/release-projector.yaml`), a level-triggered
assurance loop that guarantees every released digest carries a verifiable
Guardian countersignature at the public registry, paging
(`GuardianProjectorUnprojectedReleases`) when that cannot converge. While
GitHub publishers keep pushing ghcr first (see above), image bytes reach
ghcr before their countersignature does, so the projector's steady-state
work is moving signature material; at inversion it becomes the single —
and verifying — writer at the boundary. Its estate is the release manifest
(`release-manifest.yaml` in the same directory), the reviewable definition
of what Guardian releases — the postflight CLI's release channels:
promotions bump a lane in the same commit as the channel pin (the CLI
nightly Kargo stage carries the extra `yaml-update`;
`TestReleaseManifestCoversReleaseChannels` holds hand-made bumps to the
same rule). Per digest it verifies the countersignature in
zot against the committed public key
(`bootstrap/bundle/guardian-images.pub.pem` — deliberately independent of
Transit, so publication verification survives an OpenBao outage and a key
rotation is a reviewed diff here) together with its transparency-log
inclusion — exactly the check a consumer's stock `cosign verify --key`
performs — then copies the subject and exactly the countersignature
referrers by digest with regctl, refusing otherwise. A valid-but-unlogged
countersignature only holds its digest back until the countersigner
re-signs; an invalid one fails the run. The
copy is deliberately selective twice over: plain image copies — and
`cosign copy`, which does not carry bundle-format referrers — silently
drop the countersignature, while a blanket `--referrers` copy would let
anything a zot-write-capable attacker attached to a released digest ride
the projector's credential to the public registry. Marketplaces that only
accept CI-platform OIDC provenance (npm, PyPI) keep their keyless
publishes; the Guardian signature is the OCI lane's release signature.

The inversion itself is gated on: in-cluster CI capacity, a passed re-cache
drill (wipe the PVC, watch on-demand sync repopulate under the canary — the
drill must also confirm countersignature referrers are re-mintable
afterwards, since a wiped PVC loses them until the countersigner's next
runs re-sign), and renegotiating the rebuildable-cache invariant above,
which stops holding the moment zot is the only copy of anything.

GC note for future writers: the estate pulls by digest, so mirrored
manifests are stored untagged — and zot's default when GC is on and no
retention policy is configured is `deleteUntagged: true` (verified against
v2.1.18 source and reproduced live: a by-digest-synced manifest was deleted
on the first GC pass under a bare config). The shipped retention block
pinning `deleteUntagged: false` is therefore load-bearing; removing it
deletes the cache. `deleteReferrers` must also stay false so
countersignatures survive their subjects' churn.
