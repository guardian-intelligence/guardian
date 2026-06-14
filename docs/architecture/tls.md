# Guardian Public TLS

Status: current state, 2026-06-13.

Guardian does not run Caddy, nginx, custom TLS proxies, or per-service
sidecars. The live edge is Cilium Gateway API. Crossplane owns the platform
Gateway API objects that define the shared edge; products own the routes that
attach to that edge.

## Modes

Guardian has two public TLS modes:

- Product passthrough: Cilium `TLSRoute` routes by SNI, the backend terminates
  TLS, and the product keeps certificate/key custody. This is the aisucks and
  status model.
- Platform termination: Cilium `HTTPS` listeners terminate TLS at the edge,
  cert-manager owns certificate lifecycle, and `HTTPRoute` sends cleartext HTTP
  to the in-cluster platform service. This is the
  `oci.guardianintelligence.org` model.

Platform termination uses automated per-host certificates. Do not use a
wildcard or shared SAN certificate unless the owning surface records that blast
radius tradeoff.

## Ownership

`guardian up` installs a pinned Crossplane substrate for Gateway ownership:

- Crossplane v2.3.2 controller image runs from the seed registry.
- `provider-kubernetes` v1.2.1 is installed by Crossplane package manager with
  a digest-pinned xpkg.
- `function-go-templating` v0.12.1 is installed by Crossplane package manager
  with a digest-pinned xpkg.
- `function-auto-ready` v0.6.5 is installed by Crossplane package manager with
  a digest-pinned xpkg so the `EdgeGateway` composite Ready condition follows
  the composed provider-kubernetes `Object`s.
- `platform.guardian.dev/EdgeGateway` is a cluster-scoped XRD.

The `EdgeGateway` composition owns:

- `Namespace/gateway`.
- `GatewayClass/cilium`.
- `Gateway gateway/edge`.
- `ClusterIssuer/letsencrypt-production` when platform TLS is requested.
- One cert-manager `Certificate` per platform-terminated hostname.

Applications own routes in their own namespaces:

- `src/platform/public-http-service/` owns aisucks `TLSRoute` and `HTTPRoute`.
- `src/status/` owns status `TLSRoute`.
- `src/infrastructure-components/zot/` owns the OCI `HTTPRoute`.

`EdgeGateway` must not own product routes. Products must not own Gateway
listeners, Gateway certificate refs, or platform TLS `Certificate` resources.
That is the boundary that avoids multiple renderers mutating the same shared
Gateway listener list.

## Bootstrap

Fresh bootstrap order for a Gateway-enabled site:

1. Talos inline manifests install Gateway API CRDs, then Cilium.
2. `guardian up` starts the seed registry.
3. Workspace-built images are pushed to the seed registry by digest.
4. OpenBao is converged and unsealed/configured.
5. Crossplane is applied and its CRDs/controllers are waited on.
6. cert-manager is applied if platform TLS is requested.
7. provider-kubernetes, function-go-templating, and function-auto-ready
   packages are applied and waited on.
8. ProviderConfig and the EdgeGateway XRD/Composition are applied.
9. `guardian up` applies the checked-in manifests listed in
   `gateway.manifests`, including the site's concrete EdgeGateway.
10. Product components apply their Deployments/Services/routes.

`guardian up` may wait for Kubernetes objects and controllers to converge, but
fresh ACME issuance is not part of the four-minute host bootstrap SLA. Public
certificate issuance depends on DNS, ACME CA state, and rate limits. The CLI
records TLS convergence separately during drills.

## DNS-01

Platform hostnames under `guardianintelligence.org` use Cloudflare DNS-01.
HTTP-01 was rejected for this topology because cert-manager's in-cluster
self-check to the public hostname timed out on the single-node host-network
edge. DNS-01 avoids same-node public-IP hairpin behavior.

`guardian up` creates or updates the cert-manager Cloudflare token Secret from
operator environment or the gitignored `./secret.env` file without printing the
token. The preferred variable is:

```sh
CLOUDFLARE_GUARDIAN_INTELLIGENCE_ORG_DNS_ZONE_API_TOKEN
```

The current gitignored lowercase `secret.env` spelling is accepted for
compatibility, and environment variables take precedence over file values.

## Survival Set

Disaster recovery of an already-enrolled hostname should serve from restored
cert material and let cert-manager renew asynchronously. The platform TLS
survival set is:

- The cert-manager ACME account key Secret.
- The per-host TLS Secret in `gateway`.
- The Cloudflare DNS-01 token Secret in `cert-manager`, or the operator
  environment needed for `guardian up` to recreate it.
- The checked-in site `EdgeGateway` manifest that lets Crossplane and
  cert-manager adopt or renew the restored Secret.

## ListenerSet

Gateway API `ListenerSet` remains future state. It is the right eventual
aggregation mechanism for independently owned listeners, but it is not usable
on the current Cilium 1.19.4/Gateway API v1.4.1 pin because Cilium does not
reconcile the kind there.

Do not migrate until all of these are true:

- The Cilium ListenerSet implementation is merged.
- A Cilium release or release candidate includes it.
- Cilium docs list `ListenerSet` as supported and state the required Gateway
  API CRD version.
- The release notes identify the `TLSRoute` migration requirement.
- A dev wipe drill proves ListenerSet Accepted/Programmed status and external
  HTTPS routing through hostNetwork Envoy.

The future ListenerSet PR must update Cilium values/render, Gateway API CRDs,
route API versions, render tests, and this document in one pinned version
ratchet.

## Verification

Local/schema checks:

```sh
bazelisk test //src/guardian-cli/cmd/guardian:guardian_test
bazelisk build //:build
```

Gateway checks:

```sh
kubectl --kubeconfig ~/.local/state/guardian/guardian-dev/kubeconfig \
  get edgegateway,gatewayclass,gateway -A
kubectl --kubeconfig ~/.local/state/guardian/guardian-dev/kubeconfig \
  get httproute,tlsroute -A
kubectl --kubeconfig ~/.local/state/guardian/guardian-dev/kubeconfig \
  get certificate -A
```

Platform TLS checks:

```sh
curl -v https://oci.guardianintelligence.org/v2/
openssl s_client -connect oci.guardianintelligence.org:443 \
  -servername oci.guardianintelligence.org </dev/null
```

OCI checks:

```sh
oras discover oci.guardianintelligence.org/<repo>@sha256:<manifest>
cosign verify oci.guardianintelligence.org/<repo>@sha256:<manifest>
```
