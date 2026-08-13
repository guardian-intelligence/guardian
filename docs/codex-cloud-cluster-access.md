# Codex cloud cluster access

For environment selection, authority comparison, and the preferred routes for
local, provider-hosted, self-hosted, and in-cluster agents, start with
[Agent environment authentication](agent-environment-authentication.md). This
page is the setup and proof runbook for the current Codex Cloud route.

Codex cloud reaches the Guardian management cluster through a dedicated
Cloudflare Tunnel. Cloudflare Access authenticates the environment's transport
with a service token; Kubernetes authenticates the agent as the existing
`platform-agent` read persona. The persona can inspect Flux, Kargo, Flagger,
workloads, and cluster-scoped state, cannot read Secrets, and is denied every
write except pod port-forward and TokenRequests for explicitly declared,
15-minute product capabilities. Wake Up Mythra exposes the first pair through
`aspect mythra`: a read-only journal observer and a pod-restart operator.

The two boundaries are independent:

1. `guardian-mgmt-dns` owns the Tunnel, proxied DNS record, Access application,
   policy, and service token.
2. Flux owns the two `cloudflared` connectors in `external-dns`. Their token is
   relayed from the OpenTofu output into OpenBao at
   `guardian/guardian-mgmt/external-dns/codex-cloud-tunnel` and projected by
   External Secrets.

## Codex environment

Create the environment at
<https://chatgpt.com/codex/cloud/settings/environment/create> and select the
Guardian repository. Use these scripts:

- Setup: `scripts/codex-cloud-setup.sh`
- Maintenance: `scripts/codex-cloud-maintenance.sh`

Add four encrypted environment secrets:

- `GUARDIAN_CODEX_KUBECONFIG_B64`: portable kubeconfig with
  `__GUARDIAN_HOME__` and `__GUARDIAN_REPO_ROOT__` placeholders. Its cluster
  server is `https://127.0.0.1:16443` and `tls-server-name` is
  `k8s.guardianintelligence.org`.
- `GUARDIAN_CODEX_OIDC_CACHE_TGZ_B64`: a gzip tar of the contents of the
  `platform-agent` kubelogin cache directory.
- `CF_ACCESS_CLIENT_ID`: `codex_cloud_access_client_id` from the
  `guardian-mgmt-dns` OpenTofu root.
- `CF_ACCESS_CLIENT_SECRET`: `codex_cloud_access_client_secret` from the same
  root.

Secrets are consumed only by setup. It writes mode-0600 runtime files into the
environment before Codex removes setup secrets. The maintenance script restarts
the local Access TCP proxy when a cached environment is resumed.

## Proof

In a cloud task, require all of the following:

```sh
tools/ops/codex-cloud-tunnel status
kubectl auth whoami
kubectl get kustomizations.kustomize.toolkit.fluxcd.io -A
kubectl get stages.kargo.akuity.io,warehouses.kargo.akuity.io,promotions.kargo.akuity.io -A
kubectl get canaries.flagger.app -A
aspect mythra status
! kubectl get secrets -A
! kubectl create namespace codex-cloud-write-denial --dry-run=server -o name
```

The route is proven only when reads return live objects and both negative
checks are denied by the API server.
