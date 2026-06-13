# Edge gateway (roadmap M3)

Record of decision, ratified 2026-06-12. Phase 0 (CRD vendoring, values
delta, re-render, machine-config wiring, ordering regression test) is in
the tree; nothing is deployed — inline manifests are bootstrap-only, so the
working-tree state is inert for every running site. Companions:
`docs/architecture/topology.md` (gates the prod conversion),
`docs/runbooks/cilium-conversion.md` (the drilled wipe fallback). This doc
changes by amendment — the dev pilot record at the bottom (2026-06-12/13)
supersedes "nothing is deployed": dev is converted and serving through the
Gateway.

## Why the Gateway is the keystone

:443 has been app-owned: the aisucks binary binds the host port via
hostNetwork and is the only thing that can answer on a site's address. SNI
routing turns the port into a platform resource, and five roadmap items
queue behind exactly that: the OCI registry hostname (M7), the status page
(M4), weighted canary backends (M6), `replicas: 2` (ends the SO_REUSEPORT
scrape-aliasing constraint recorded in observability.md), and the app's
move to pod network — which is what gives it a pod identity the `toFQDNs`
egress lockdown can police. One listener decoupled from app lifecycle;
everything else is a route.

## Decisions

- **Cilium Gateway API, served by the node Envoy** (the `cilium-envoy`
  DaemonSet already running). One control plane, no sidecars, no
  cert-manager, no separate gateway fleet. The values delta is
  `gatewayAPI.enabled` + `hostNetwork.enabled` plus `NET_BIND_SERVICE` on
  the Envoy wrapper (`src/infrastructure-components/cilium/values.yaml`,
  which records each constraint inline).
- **hostNetwork mode on the /31.** Single-node sites with a routed
  point-to-point /31 have no shared L2 and no LB to program — the
  LB-IPAM / BGP menu returns only with the 3-node topology (topology.md).
  Gateway and Route objects survive that growth unchanged: BGP becomes a
  layer that attracts a floating IP to a node where the same hostNetwork
  Envoy listens — IP attraction, not a redesign.
- **TLS passthrough by default** (TLSRoute, SNI routing). Envoy never
  decrypts; services keep in-process certmagic and their own key custody.
  Connections with no matching SNI drop at the edge. Per-hostname
  termination (HTTPRoute) is a later, reversible, per-hostname choice —
  revisit when key custody exists (M7's bao Transit), e.g. a status page
  terminating at the Gateway while aisucks stays passthrough.
- **CRD pin = a function of the Cilium version.** Cilium 1.19 consumes
  TLSRoute as v1alpha2, so we vendor the exact Gateway API release the
  Cilium 1.19.4 docs declare conformance against (v1.4.1 at this writing;
  the pin of record, channel rationale, and per-file sha256s live in the
  header of
  `src/infrastructure-components/cilium/talos/gateway-api-crds-inline.yaml`).
  Gateway API v1.5 graduated TLSRoute to v1 and its standard CRD stops
  serving v1alpha2 — bumping the CRDs alone would silently disable the path
  everything routes through. Adopt v1 at the Cilium bump that does, as one
  change.
- **:80 listener**: HTTPRoute forwarding `/.well-known/acme-challenge/`
  (HTTP-01) plus the HTTPS redirect. TLS-ALPN-01 rides passthrough
  natively.

## The bootstrap invariant (the acceptance bar)

Operator's words: bootstrapping still works and converges, in order, on
Cilium firewall + gateway. Concretely:

- **CRDs apply before Cilium, provably.** The CRDs ship as their own
  Talos inline manifest listed immediately above `cilium-inline.yaml` in
  every site.yaml; list order is `cluster.inlineManifests` order.
  `src/guardian-cli/cmd/guardian/render_order_test.go` renders each site's
  machine config with the pinned talosctl — the same `gen config` up.go
  issues — and fails if `gateway-api-crds` does not precede `cilium`.
  Nothing in Go enforces the order; the test is what a careless site.yaml
  edit trips.
- **Inert by construction.** Talos applies inline manifests at bootstrap
  only; a config-only re-render is a no-op for a running site. So Phase 0
  carries zero rollout risk, and the rollout *is* the per-site conversion
  below — a fresh bootstrap converges CRDs → Cilium+gateway → firewall
  with no operator in the loop.
- **The firewall is unchanged and out of scope for CNPs.** The admin plane
  (apiserver :6443, apid :50000) is allowed by the Talos host nftables
  rules (`ingress-firewall.yaml`) and is never policed by
  CiliumNetworkPolicies — the lockout-proofing lives one layer below
  anything this design touches.

## Conversion (per site, dev → gamma → prod)

Enabling `gatewayAPI` on the same Cilium version is a config delta: update
the machine config AND `kubectl apply` the identical re-render live (CRDs
first) — no drift, no wipe. The app's move off host :443 is the part with
teeth, and the dev pilot exists to answer two empirical questions:

1. **SO_REUSEPORT overlap (the hitless-handover hypothesis).** The app
   already binds :443 with SO_REUSEPORT; Envoy listeners enable
   `reuse_port` by default. If both hold, the kernel splits new
   connections across app and Envoy during coexistence: the old
   hostNetwork pod serves its share directly while Envoy SNI-routes its
   share to the new pod-network pods (same hostPath cert cache; certmagic
   file-locks). Scale the old Deployment to zero → handover completes with
   zero drops. Hypothesis, not fact, until measured.
2. **The proxy loop during overlap.** While the old pod is hostNetwork, an
   Envoy passthrough backend at nodeIP:443 lands back in Envoy's own
   reuseport group — a connection Envoy forwards can be accepted by Envoy
   again. The recursion is geometrically bounded (each hop re-rolls the
   reuseport dice), but "bounded" is not "acceptable"; the pilot measures
   it under curl flood before any site converts this way.

If either answer is bad: the **brief-gap alternative** (scale the app off
:443, then route — a sub-minute cutover at a chosen hour, spent against
the error budget), or the **drilled wipe-convert** (212s measured,
`docs/runbooks/cilium-conversion.md`) as the standing fallback either way.

## Phases

- **Phase 0 — vendoring + ordering (in tree).** CRD inline manifest,
  values delta, re-render, site.yaml wiring, regression test. VERIFY:
  `render_order_test.go` green for all three sites; `bazelisk test //...`
  green; the re-render diff touches only inline manifests (inert for
  running sites).
- **Phase 1 — gateway component + routes.** Gateway + TLSRoute/HTTPRoute
  objects as a guardian component; push.go grows manifest-only component
  support (every current component pairs an image with its manifest; the
  gateway is objects only). VERIFY: unit tests; `kubectl --dry-run=server`
  against dev validates every object against the pinned CRDs.
- **Phase 2 — app to pod network.** Drop hostNetwork, add the Service,
  `replicas: 2`. The hostPath cert cache stays correct while sites are
  single-node (certmagic file-locks across pods on one box); the 3-node
  era needs shared custody — bao Transit or Gateway termination — noted
  here so topology growth doesn't rediscover it. VERIFY: per-pod scrape
  identity in VM (no counter interleaving); both replicas serve through
  Envoy.
- **Phase 3 — dev pilot.** Convert dev; one deliberate wipe proving a
  fresh bootstrap converges with the Gateway live; verify no-SNI
  connections drop at the edge; curl-flood measurement of the overlap
  window (questions 1 and 2 above). VERIFY: full release gate through
  Envoy; induced pod kill drops zero requests; wipe drill within SLA;
  firewall posture byte-identical.
- **Phase 4 — gamma.** Repeat the gate battery through Envoy across
  multiple releases, plus one forced ACME renewal proving issuance works
  through passthrough — renewal is the failure mode you otherwise discover
  60 days late. VERIFY: repeated gates green; forced renewal mints a cert
  with no manual step.
- **Phase 5 — prod.** Conversion mode (hitless / brief-gap / wipe) chosen
  from pilot data, sequenced with topology.md's migration gates — prod
  edge surgery happens once, inside the 1→3 growth, never before.
  VERIFY: gate battery; corpus dump/restore invariant per the conversion
  runbook.
- **Phase 6 — egress lockdown.** Default-deny CiliumNetworkPolicies with
  `toFQDNs` for the app's named upstreams; admin plane untouched by
  construction. VERIFY: app functions with the allowlist; a non-allowlisted
  egress visibly drops in Hubble metrics.

**Riders.** BBR and netkit are deferred to the dev pilot — adopted only if
they measure clean, one values line each. XDP acceleration is dropped, not
deferred: it accelerates the NodePort/LoadBalancer datapath, and a
hostNetwork Envoy listener is a plain host socket — there is nothing on
that path for XDP to accelerate.

## Risks and open threads

- **Gatus probes dev by raw IP** — no SNI, so a fully SNI-routed :443
  would drop the prober that watches the pilot. Dev routes stay
  host-unrestricted (or Gatus moves to hostname probes) until the
  cross-site blackbox migration retires Gatus (observability.md).
- **ACME renewal is the late-fuse failure.** A passthrough misroute of
  TLS-ALPN-01 or the :80 HTTP-01 path stays invisible until certs age —
  hence the forced-renewal gate in Phase 4, on gamma, before prod ever
  converts.
- **The proxy loop** (above) is hypothesized bounded, not proven
  acceptable; the brief-gap mode is the pre-agreed exit if the measurement
  is ugly.
- **The CRD pin is a trap for the diligent**: an innocent "bump Gateway
  API to latest" silently kills TLSRoute on Cilium 1.19. The vendored
  file's header says so; this doc says so twice.

## Dev pilot record (Phase 3) — executed 2026-06-12/13 UTC, by amendment

Dev is converted: Envoy owns :80/:443 (24 reuseport worker sockets),
aisucks serves pod-network with `replicas: 2` behind the TLSRoute, the
status page serves behind its own TLSRoute with a real Let's Encrypt
certificate, and one deliberate wipe proved a fresh bootstrap converges
the fully-converted state unattended. Numbers below; adjectives omitted.

**The ordering lesson, learned by outage.** The pod-network flip
(`guardian up` at 23:05:53Z) ran BEFORE the live cilium re-render + control
plane restart that arms the gateway. Envoy held no NET_BIND_SERVICE and no
gateway config; the hostNetwork pod was already gone — :443 refused
connections for **12.1 minutes** (23:05:53–23:18:02Z). Pages fired and are
recorded honestly: dev gatus self ×2, gamma+prod gatus watchers, vmalert
`SiteProbeFailed` ×2; all resolved by 23:19Z. Recovery was completing the
arming live: `kubectl diff` showed exactly the predicted 5 objects, apply +
envoy DS roll + operator/agent restart; first 200 at 23:18:02Z. The
per-site conversion order is therefore non-negotiable and now drilled:
**arm first (re-render apply + operator/agent restart), flip second.**

**Question 1 (SO_REUSEPORT overlap): NOT measured.** Because the flip ran
unarmed, app and Envoy never coexisted on :443 — there was no overlap
window, there was an outage. The hypothesis remains open and must be
measured during gamma's (correctly ordered) flip under flood; brief-gap
stays the pre-agreed exit.

**Question 2 (proxy loop): clean, structurally and observed.** Node IP
appeared in the aisucks EndpointSlices **0 times** (sampled post-cutover
and post-wipe; the `aisucks.app/network: pod` selector guard held). Hubble
on :443 under flood: zero Envoy/host-originated flows to nodeIP:443;
Envoy's upstream connections originate from cilium_host, not client IPs.

**New upstream finding — the pod→gateway hairpin is broken.** A pod
connecting to the node address reaches Envoy's kernel socket (handshake
completes) but cilium-envoy never services a connection that did not enter
via Cilium's proxy redirect — 100% of pod-originated requests to
:80/:443 hang; external and host-netns clients are unaffected (upstream
cilium/cilium#36004 family; verified live on 1.19.4). Consequence: the
in-cluster Gatus self-probes went red and paged the moment aisucks left
the host netns. Standing fix (commit `86fcd7f`): an `aisucks-probe`
Service with pinned ClusterIP 10.96.111.43 + Gatus `hostAliases` mapping
the site domain to it — same URLs, same SNI, full TLS verification;
self-probes measure app + certificate, the edge is the sibling watchers'
vantage (which stayed green-on-truth throughout). This replaces the
"Gatus probes dev by raw IP" risk above: raw-IP probing is dead, the
hairpin is dead, the alias is the mechanism.

**Battery results (post-conversion, re-run post-wipe).** Release gate
through Envoy: healthz 200 (0.05–0.20s), page marker present, and the
then-current API probe returned the expected payload. No-SNI
connections drop with no certificate leaked; bogus SNI resets; matched SNI
presents CN=dev.aisucks.app. :80 — raw-IP healthz 200 (hostname-less
HTTPRoute), domain 308→https, stale ACME token 308 fallthrough. Scrape
identity: `up{job="aisucks"}` = 2 series, instance = pod names, balanced
(0.43/0.42 rps). Firewall byte-identical: 80/443/6443/50000 open,
9964/9965/4244/4240 blocked. Induced pod kill under ~20 rps flood with
`Connection: close`, run twice: **1200 requests → 2 failures** and
**700 requests → 2 failures**, every failure inside ≤0.27s of the delete —
the app closes its listener on SIGTERM a beat before Envoy drops the
endpoint. Not zero-drop; follow-up: delay listener close until readiness
failure propagates (preStop sleep or shutdown reorder in main.go).

**The deliberate wipe.** `down` 68s to maintenance; `up` converged in
**129s** (apiserver + namespaces at 122s; secrets recreated immediately);
gateway converged from bootstrap with **zero manual kubectl**: GatewayClass
`cilium` Accepted=True, TLSRoute/HTTPRoute Accepted=True, Envoy holding
:80/:443, no-SNI drop verified. Postgres dump/restore invariant held.
Ledger: DDL re-applied, Converged marker durable in ClickHouse,
`k8s.cluster.name = guardian-dev`. But the 4-minute serving SLA was
**missed: first 200 at +621s** — root cause is an ACME rate limit, not the
bootstrap: the morning outage left 5 failed TLS-ALPN authorizations/hour
for dev.aisucks.app (pods retried issuance while :443 was dead), so the
fresh pods' issuance got HTTP 429 and aisucks treats certificate failure
as fatal (CrashLoopBackOff, PodRestartStorm fired). Recovered by restoring
the pre-wipe cert-cache tarball into the hostPath via a helper pod —
serving 17s later, no issuance spent. Standing rule for every wipe drill:
**tar /var/lib/aisucks-certs and /var/lib/status-certs before `down`**;
the failed-authorization limit (5/h) is the sharp cert budget, not just
duplicate-certs 5/week.

**Status quirk, recorded.** In hostNetwork mode Cilium 1.19.4 never
populates a Gateway address: `Programmed=False / AddressNotAssigned` and
listeners `Programmed=False/Pending` while the datapath fully serves
(listeners Accepted=True, routes attached, sockets bound). Bootstrap
acceptance must read GatewayClass Accepted + listener Accepted +
attachedRoutes + the socket census — not Gateway Programmed.

**Exit state (2026-06-13 00:03Z).** Gatus all green including the API
probe; vmalert quiet except PodRestartStorm aging out on the two deleted
crash-loop pods; both replicas scraped per-pod and receiving traffic;
status.toml/.json/ + HTML serve over verified TLS. Gamma's conversion
inherits: arm-then-flip ordering, the probe-alias render, the cert-cache
backup rule, and the still-open overlap measurement.
