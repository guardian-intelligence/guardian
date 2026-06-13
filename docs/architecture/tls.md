# Guardian Public TLS Handoff

Status: implementation handoff, 2026-06-13.

This is the TLS prerequisite for the public OCI registry and future platform
APIs. It deliberately does not add Caddy, nginx, a custom TLS proxy, or
per-service sidecars. The live edge remains Cilium Gateway. Crossplane owns the
durable platform API that declares what the gateway must serve.

## Decision

Guardian has two TLS modes:

- Product passthrough: Cilium `TLSRoute` routes by SNI, the backend terminates
  TLS, and product services keep certificate/key custody. This is the current
  aisucks/status pattern.
- Platform termination: Cilium `HTTPS` listeners terminate TLS at the edge,
  cert-manager owns certificate lifecycle, and `HTTPRoute` sends cleartext to
  the in-cluster platform service. This is the target mode for
  `oci.guardianintelligence.org`.

The public OCI registry is a platform API, not a product app, so it should use
Gateway-terminated TLS.

Platform termination uses automated per-host certificates by default. Do not use
a wildcard or shared SAN certificate unless a specific surface needs that blast
radius tradeoff and records it. The operator-facing API declares hostnames; it
does not ask callers to hand-maintain certificate objects.

## Why This Is A Separate Slice

The OCI registry can be a normal zot Deployment and Service, but
`oras pull oci.guardianintelligence.org/...` is only real when TLS custody is
solved. The registry slice must not smuggle in a second public edge binary to
get certificates working. TLS is a platform capability with its own API, status,
backup story, and failure modes.

## Crossplane API Shape

Use one cluster-scoped composite resource to own the shared Gateway object.
Do not let many product/platform XRs mutate the same `Gateway` listeners.
Crossplane v2 namespaced XRs can compose resources only in their namespace;
the shared gateway must create resources in `gateway`, product namespaces, and
platform namespaces, so this XR should be `scope: Cluster`.

```yaml
apiVersion: platform.guardian.dev/v1alpha1
kind: EdgeGateway
metadata:
  name: default
spec:
  gatewayClassName: cilium
  namespace: gateway
  http:
    enabled: true
    rawIpHealthz: true
  certificateIssuer:
    kind: ClusterIssuer
    name: letsencrypt-production
  listeners:
    - name: aisucks
      hostname: dev.aisucks.app
      port: 443
      tls:
        mode: Passthrough
      route:
        kind: TLSRoute
        namespace: aisucks
        service: aisucks
        servicePort: 443
    - name: status
      hostname: status.guardianintelligence.org
      port: 443
      tls:
        mode: Passthrough
      route:
        kind: TLSRoute
        namespace: status
        service: status
        servicePort: 443
    - name: oci
      hostname: oci.guardianintelligence.org
      port: 443
      tls:
        mode: Terminate
        secretName: oci-guardianintelligence-org-tls
      route:
        kind: HTTPRoute
        namespace: guardian-oci
        service: zot
        servicePort: 5000
```

The composition owns:

- `Namespace/gateway`.
- `gateway.networking.k8s.io/Gateway gateway/edge`.
- TLS passthrough listeners and `TLSRoute` resources for product-owned TLS.
- HTTPS terminated listeners and `HTTPRoute` resources for platform APIs.
- `cert-manager.io/ClusterIssuer` or a reference to a separately owned issuer.
- One cert-manager `Certificate` per terminated hostname, in the Gateway
  namespace, with a Secret referenced by that hostname's HTTPS listener.
- Route status projection back into `EdgeGateway.status`.

Do not introduce a separate `PublicTLS` XR until there is a safe aggregation
mechanism. Gateway API `ListenerSet` is that mechanism, but it is not usable on
the current Cilium 1.19.4/Gateway API v1.4.1 pin: Cilium does not reconcile the
kind there.

`PublicHttpService` may own same-namespace `TLSRoute`/`HTTPRoute` objects once
the shared listener exists: routes attach to `gateway/edge`, but they do not
mutate its listener set. It must not own Gateway listeners, certificate refs, or
platform TLS `Certificate` resources until there is a safe listener aggregation
story. Gateway API `ListenerSet` is the likely future mechanism; before that,
listener and cert ownership stay centralized in `EdgeGateway` or the
direct-render bridge.

## Direct-Render Bridge

Crossplane is not installed by `guardian up` yet. The first implementation can
land in two layers:

1. Add the Crossplane `EdgeGateway` XRD/Composition/example as the API of
   record.
2. Extend the existing direct-rendered gateway component to render the same
   objects from `site.yaml` until Crossplane itself is pinned, mirrored, and
   installed.

The bridge must be temporary and byte-for-byte shaped like the future
composition. Do not create a second gateway implementation.

The bridge must preserve the bootstrap contract. `guardian up` may wait for the
Gateway, cert-manager, routes, and backend rollouts to exist, but it must not
make fresh ACME issuance a hard prerequisite for converge. Public certificate
issuance depends on DNS, the ACME CA, and rate-limit state, so it is not bounded
by the four-minute host bootstrap SLA.

Repeat wipe drills for an existing hostname must restore existing TLS material
instead of spending ACME issuance. The survival set for platform TLS is:

- The cert-manager ACME account key Secret.
- The per-host TLS Secret in `gateway`.
- The Cloudflare DNS-01 token Secret in `cert-manager`, or the operator
  environment needed for `guardian up` to recreate it.
- The rendered `EdgeGateway`/Certificate spec that lets cert-manager adopt or
  renew the restored Secret.

First enrollment of a brand-new hostname may issue through ACME. Disaster
recovery of an already-enrolled hostname should serve from restored cert
material and let cert-manager renew asynchronously.

Decision, measured 2026-06-13: platform hostnames under
`guardianintelligence.org` use DNS-01 through Cloudflare, not HTTP-01 through
the Gateway. The HTTP-01 solver route for `oci.guardianintelligence.org`
returned `200` from outside the cluster, but cert-manager's in-cluster
self-check to the same public hostname timed out repeatedly. That makes HTTP-01
depend on same-node public-IP hairpin behavior, so it is the wrong default for
this single-node host-network edge.

`guardian up` creates or updates the cert-manager token Secret from operator
environment without printing the token. The preferred variable is
`CLOUDFLARE_GUARDIAN_INTELLIGENCE_ORG_DNS_ZONE_API_TOKEN`; the current
gitignored `secret.env` lowercase spelling is accepted for compatibility.

## ListenerSet Upgrade Track

As of 2026-06-13, no released Cilium version is acceptable as the Guardian
ListenerSet target.

Current upstream state:

- Cilium 1.19.4 supports Gateway API v1.4.1 and does not support
  `ListenerSet`.
- Cilium 1.20.0-pre.2 moved Gateway API support to v1.5.1 and requires
  `TLSRoute` v1, but did not add ListenerSet reconciliation.
- Cilium 1.20.0-pre.3 still does not document `ListenerSet` support.
- Cilium PR `cilium/cilium#46303` (`GatewayAPI: Implement ListenerSets`) is open
  against `main`, claims upstream ListenerSet conformance passes, and fixes
  `cilium/cilium#42756`. It is not merged, released, or documented yet.
- cert-manager 1.20 supports ListenerSet certificate generation as an alpha
  feature behind the ListenerSet feature gate; it is necessary but not
  sufficient because Cilium must also reconcile ListenerSets into Envoy config.

Upgrade only when all of these are true:

1. The Cilium ListenerSet implementation is merged.
2. A Cilium release or release candidate includes it.
3. Cilium docs list `ListenerSet` in supported Gateway API resources and state
   the required Gateway API CRD version.
4. The release notes identify the `TLSRoute` migration requirement.
5. A local dev wipe drill proves `ListenerSet` Accepted/Programmed status and
   external HTTPS routing through hostNetwork Envoy.

The pinned-version bundle for that PR is:

- Cilium chart/render: `src/infrastructure-components/cilium/values.yaml` and
  `src/infrastructure-components/cilium/talos/cilium-inline.yaml`.
- Gateway API CRDs:
  `src/infrastructure-components/cilium/talos/gateway-api-crds-inline.yaml`,
  including `listenersets.gateway.networking.k8s.io`.
- Gateway route manifests:
  `src/infrastructure-components/gateway/k8s/gateway.yaml.tmpl` must migrate
  `TLSRoute` from `gateway.networking.k8s.io/v1alpha2` to `v1` if the selected
  Cilium release follows the v1.5.1 behavior.
- Render tests and docs that pin the old `TLSRoute` version:
  `src/guardian-cli/cmd/guardian/render_gateway_test.go`,
  `docs/architecture/gateway.md`, and this file.
- cert-manager manifests once platform TLS is installed: pin chart/manifests,
  images, Gateway API support, and ListenerSet feature gate together.

Do not batch unrelated toolchain upgrades with this substrate change unless the
Cilium compatibility matrix requires it. Talos/Kubernetes, Bazel, Go, and
application image pins should move in their own ratchets so the convergence
delta can be attributed.

## Convergence Delta Report

The current measured dev baseline in `docs/architecture/gateway.md` is:

- `guardian down`: 68s to maintenance.
- `guardian up` from maintenance: 129s to converged Kubernetes resources.
- Apiserver + namespaces: 122s.
- First public 200: 621s because a prior outage exhausted Let's Encrypt failed
  authorization budget. This is an ACME/cert-cache failure, not a Cilium
  convergence baseline.

The ListenerSet upgrade report must separate substrate convergence from public
certificate issuance:

- Run at least three dev wipe drills on the current pin and the candidate pin,
  with the same image-cache posture and restored certificate material.
- Record `guardian up` wall time, node Ready, seed-registry rollout, component
  apply completion, GatewayClass Accepted, Gateway listener Accepted,
  ListenerSet Accepted/Programmed, Route attached, host `:80/:443` socket
  census, first external SNI 200, and all-pods-running tail.
- Report median and max for each checkpoint.
- Report deltas as `candidate - baseline` for:
  `node-ready`, `gateway-ready`, `first-public-200`, and `all-running`.
- Report ACME issuance separately and never count a fresh ACME authorization as
  part of the four-minute bootstrap SLA.

Expected files:

```text
src/crossplane/configurations/guardian-platform/
  README.md
  apis/edge-gateway.xrd.yaml
  examples/edge-gateway.yaml
  compositions/edge-gateway-cilium.composition.yaml

src/crossplane/configurations/guardian-products/
  apis/public-http-service.xrd.yaml
  apis/aisucks-product.xrd.yaml

src/infrastructure-components/gateway/k8s/gateway.yaml.tmpl
src/guardian-cli/cmd/guardian/site.go
src/guardian-cli/cmd/guardian/render_gateway_test.go
src/guardian-cli/cmd/guardian/render_order_test.go
docs/architecture/gateway.md
docs/architecture/tls.md
docs/releases.md
```

## cert-manager Requirements

Use cert-manager as the certificate lifecycle controller. It is a controller,
not a sidecar, and it is the standard boring integration point for Gateway API
certificates.

Implementation requirements:

- Pin cert-manager image/chart/manifests by version and digest.
- Mirror the artifacts through Guardian release artifacts before fleet install.
- Enable cert-manager Gateway API support.
- Keep Gateway API CRDs ordered before cert-manager startup.
- Use Cloudflare DNS-01 for `oci.guardianintelligence.org`; HTTP-01 is a known
  bad fit on the current single-node topology because cert-manager's self-check
  cannot rely on hairpinning through the site's public IP.

The ACME path must not break existing raw-IP `:80 /healthz` behavior recorded
in `docs/architecture/gateway.md`.

## Cilium Gateway Requirements

The implementation stays on Cilium Gateway API:

- Keep hostNetwork Envoy as the public edge for single-node `/31` sites.
- Keep existing TLS passthrough listeners for aisucks/status.
- Add a terminated HTTPS listener for platform hostnames such as
  `oci.guardianintelligence.org`.
- Use `HTTPRoute` from the HTTPS listener to `guardian-oci/zot`.
- If Cilium rejects mixed `TLS` passthrough and `HTTPS` termination listeners
  on the same host-network `:443`, treat that as a blocked compatibility gate:
  do not assume two Gateway objects can safely share the same host port. Resolve
  by proving a supported Cilium configuration, upgrading Cilium/Gateway API, or
  deferring platform termination. Do not add another proxy.

Acceptance must continue to use the repo's learned Cilium signals:

- `GatewayClass` Accepted.
- Listener Accepted.
- Route attached.
- Host socket census for :80/:443.
- External TLS verification.

Do not rely on `Gateway.status.addresses` or `Programmed=True` on the current
hostNetwork Cilium shape; the gateway doc records that status quirk.

## Security Boundary

Public TLS termination means the platform Gateway owns private keys for
platform hostnames. Product services that require app-owned keys stay on
passthrough.

For OCI:

- TLS key custody: cert-manager Secret in `gateway`.
- Registry auth: zot bearer auth with GitHub OIDC for writer authority.
- Public pull: anonymous.
- Push: short-lived GitHub OIDC token whose claims restrict repository,
  workflow/environment, branch/ref, and audience.
- npm Trusted Publishing: separate GitHub-hosted OIDC path. Do not reuse
  `NPM_TOKEN` for OCI registry writes.

The future zot writer policy should accept only the configured release
workflow identity and only the `guardian/aisucks/sdk/npm` repository until more
release targets exist.

## Verification Battery

Local/schema checks:

```sh
bazelisk test //src/guardian-cli/cmd/guardian:guardian_test
bazelisk build //:build
```

Gateway dry-run against a converted site:

```sh
guardian up src/sites/dev/site.yaml
kubectl --kubeconfig ~/.local/state/guardian/guardian-dev/kubeconfig \
  get gateway -n gateway edge -o yaml
kubectl --kubeconfig ~/.local/state/guardian/guardian-dev/kubeconfig \
  get httproute,tlsroute -A
```

TLS checks for a platform-terminated hostname:

```sh
curl -v --resolve oci.guardianintelligence.org:443:<node-ip> \
  https://oci.guardianintelligence.org/v2/
openssl s_client -connect <node-ip>:443 \
  -servername oci.guardianintelligence.org </dev/null
```

Certificate lifecycle checks:

```sh
kubectl --kubeconfig <kubeconfig> -n gateway get certificate,secret
kubectl --kubeconfig <kubeconfig> -n cert-manager logs deploy/cert-manager
```

OCI checks that become possible after zot lands:

```sh
oras pull oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest> -o ./dist
oras discover oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>
cosign verify oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>
```

## First PR Slice

The first PR should not deploy zot. It should solve the TLS API boundary:

1. Add `guardian-platform` Crossplane package skeleton.
2. Add `EdgeGateway` XRD, composition placeholder, and example.
3. Amend gateway docs with the two TLS modes.
4. Update release docs to mark public OCI as blocked on platform TLS.
5. Add direct-render tests for a terminated platform listener if the renderer
   changes in the same PR.

The second PR can install cert-manager and render/apply the terminated
listener on dev. The third PR can stand up zot behind that listener.
