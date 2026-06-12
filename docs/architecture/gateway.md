# Edge gateway (roadmap M3)

Ratified design; dev pilot is the next act. Companion:
`docs/architecture/topology.md` (which gates the prod conversion).

## Decisions

- **Cilium Gateway API in hostNetwork mode**, served by the node-level
  `cilium-envoy` DaemonSet we already run. No new control plane, no
  sidecars, no separate gateway fleet.
- **TLS passthrough by default** (TLSRoute, SNI routing): Envoy never
  decrypts; services keep certmagic and their own cert custody. Connections
  with no matching SNI drop at the edge — scanner traffic never reaches a
  service. **Termination (HTTPRoute) is a per-hostname upgrade, not a
  global mode**: revisit per hostname once key custody exists (M7's bao
  Transit), e.g. a status page terminating at the Gateway while aisucks
  stays passthrough.
- **CRD pinning**: TLSRoute is GA (Standard channel, Gateway API v1.5);
  Cilium 1.19.x still consumes the v1alpha2 CRD, so install the exact CRD
  bundle Cilium 1.19.4 is conformance-tested against. Adopt v1 at the
  Cilium bump that does. CRDs apply BEFORE the re-rendered inline manifest
  enables `gatewayAPI` (re-vendor recipe: cilium `values.yaml` header).
- **:80 listener**: HTTPRoute forwarding `/.well-known/acme-challenge/` to
  the backend (HTTP-01) plus the HTTPS redirect. TLS-ALPN-01 rides
  passthrough natively.

What the Gateway unlocks, in dependency order: :443 multiplexing per site
(zot for M7, status for M4), a platform-owned listener decoupled from app
lifecycle (real readiness-gated drains; `replicas: 2`; per-pod scrape
identity — ends the SO_REUSEPORT aliasing constraint), weighted backends
(M6 canaries), and pod-network app identity for the `toFQDNs` egress
lockdown.

## Conversion (per site, dev → gamma → prod)

- **Live apply, not wipe**: enabling `gatewayAPI` on the same Cilium
  version is a config delta. Update `cilium-inline.yaml` in the machine
  config AND `kubectl apply` the identical re-render live (CRDs first) — a
  fresh bootstrap then produces the same state, so no drift and no wipe.
  The drilled wipe-convert (`docs/runbooks/cilium-conversion.md`, ~4 min)
  is the fallback.
- **Listener handover hypothesis** (the pilot's main question): the app
  already binds :443 with SO_REUSEPORT; Envoy listeners enable `reuse_port`
  by default. If cilium-envoy's hostNetwork listener does too, the kernel
  splits new connections across both during coexistence — the old
  hostNetwork pod serves directly while Envoy passthrough-routes SNI to the
  new pod-network pods (same hostPath cert cache; certmagic file-locks).
  Scale the old Deployment to zero and the handover is complete with zero
  drops. If the hypothesis fails: a sub-minute cutover at a chosen hour,
  spent against the error budget, or the wipe drill.
- **Order**: dev pilot (including one deliberate wipe proving bootstrap
  converges with the Gateway enabled — CRD/inline-manifest ordering is the
  subtle part) → gamma, with the full release gate run repeatedly through
  Envoy → prod. **Prod waits on the topology decision**: a 3-node prod
  changes how the visitor IP reaches a live Envoy (BGP floating IP, not one
  box's /31) — never do prod edge surgery twice.

Pilot riders, adopted only if they measure clean (one values line each):
XDP acceleration (`loadBalancer.acceleration: native` — verify the NIC
driver first), BBR for pod egress, netkit datapath, Hubble visibility of
Gateway flows.

VERIFY (roadmap M3): full release gate passes through Envoy on dev; induced
pod kill drops zero requests; firewall posture unchanged (CNPs never police
6443/50000).
