# Devin and Cursor cluster access

For environment selection, authority comparison, and routes for local agents,
Codex Cloud, self-hosted agents, CI, and breakglass, start with
[Agent environment authentication](agent-environment-authentication.md). This
page is the setup and proof runbook for provider-hosted Cursor and Devin.

Provider-hosted development sessions reach the Guardian management cluster
through the existing Cloudflare TCP tunnel and a provider-specific Kubernetes
ServiceAccount. Git remains the write path: agents push a branch and open a PR;
Flux, Kargo, and Flagger apply and promote reviewed changes.

The two access boundaries are independent:

1. Cloudflare Access accepts a distinct transport service token for Cursor or
   Devin. A token can be revoked without interrupting the other provider.
2. Kubernetes accepts a TokenRequest credential for that provider's exact
   ServiceAccount. It expires within one hour and carries only the
   `guardian-persona-delivery-read` ClusterRole.

The delivery-read role can follow Flux sources and reconciliations, Kargo
freight and promotions, Flagger canaries, Kubernetes workload status, events,
and pod logs. It cannot read Secrets, RBAC, admission configuration, nodes,
storage, or Cilium state. It cannot exec, attach, port-forward, or mutate any
resource. A fail-closed ValidatingAdmissionPolicy denies every write and
CONNECT operation even if RBAC is accidentally widened later.

## Credential lifecycle

Authenticate locally with the unattended read rung, then mint the single
provider session credential without printing it:

```sh
aspect infra auth --persona=read
tools/ops/agent-cloud-token devin --output /dev/shm/guardian-devin-token
tools/ops/agent-cloud-token cursor --output /dev/shm/guardian-cursor-token
```

The read persona already carries broader cluster read authority than either
provider ServiceAccount, so minting the derived credential does not cross a
privilege boundary. RBAC pins the mint to the two exact ServiceAccounts and the
read persona's admission policy requires the Kubernetes audience and a lifetime
of no more than one hour. Store it only as a Devin `session_secret` or in the
Cursor environment immediately before one cloud run, and remove any temporary
file immediately after injection. Reusing an expired environment intentionally
fails closed.

The accepted audience is the management API server's service-account issuer,
`https://10.8.0.250:6443`, which is also the `apiServerEndpoint` in the
Cozystack platform package. The public Cloudflare hostname is transport only
and is not a Kubernetes token audience.

Each provider environment receives:

- `GUARDIAN_AGENT_PROVIDER`: `devin` or `cursor`.
- `GUARDIAN_AGENT_KUBERNETES_TOKEN`: the JIT session secret.
- `GUARDIAN_AGENT_KUBERNETES_CA_B64`: the public cluster CA bundle.
- `GUARDIAN_AGENT_CF_ACCESS_CLIENT_ID`: the matching provider output from
  `guardian-mgmt-dns`.
- `GUARDIAN_AGENT_CF_ACCESS_CLIENT_SECRET`: the matching sensitive provider
  output from `guardian-mgmt-dns`.

Run `scripts/agent-cloud-setup.sh` during initial setup and
`scripts/agent-cloud-maintenance.sh` when resuming the same environment. Setup
materializes secrets to mode-0600 files and unsets them before invoking the
repository build/tool bootstrap. It writes a named kubeconfig and never
replaces `~/.kube/config`.

## Local development

Local Cursor and Devin continue to use the interactive browser/device flow:

```sh
aspect infra auth --persona=read
```

That path keeps the Keycloak refresh cache on the laptop and does not upload it
to either provider. It is the broader operations-read persona; use the
provider-hosted delivery-read credential for cloud tasks.

## Cloud proof contract

A provider task is complete only when the provider reports success and the
session itself returns all of the following from the live cluster:

```sh
tools/ops/agent-cloud-tunnel status
kubectl auth whoami
kubectl get kustomizations.kustomize.toolkit.fluxcd.io -A
kubectl get stages.kargo.akuity.io,warehouses.kargo.akuity.io,promotions.kargo.akuity.io -A
kubectl get canaries.flagger.app -A
! kubectl get secrets -A
! kubectl create namespace cloud-agent-write-denial --dry-run=server -o name
! kubectl exec -n cozy-fluxcd deploy/source-controller -- true
```

The expected identities are
`system:serviceaccount:tenant-root:guardian-cloud-agent-devin` and
`system:serviceaccount:tenant-root:guardian-cloud-agent-cursor`.

## Durable federation follow-up

The one-hour TokenRequest path is the lowest-risk deployable path that does not
change Guardian's OpenBao initialization ceremony. The durable design removes
the local mint-and-inject step while preserving the same ServiceAccounts and
RBAC:

- Devin exchanges its native per-session, audience-bound workload OIDC token.
- Cursor exchanges its automatically refreshed one-hour AWS AssumeRole
  identity.
- OpenBao authenticates the provider identity and uses its built-in Kubernetes
  secrets engine to issue the same short Kubernetes credential.

Do not add those mounts as an ad hoc live OpenBao mutation. Guardian's OpenBao
self-init block is the structural source of truth, so adding the JWT/AWS auth
mounts and Kubernetes secrets engine belongs in a separately approved
reinitialization ceremony with a restore drill and provider-claim canaries.

Primary provider references:

- <https://docs.devin.ai/product-guides/oidc>
- <https://docs.devin.ai/product-guides/secrets>
- <https://cursor.com/docs/cloud-agent/setup>
- <https://cursor.com/docs/cloud-agent/security-network>
- <https://openbao.org/docs/auth/jwt/>
- <https://openbao.org/docs/plugins/>
