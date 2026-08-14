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
   ServiceAccount. It expires within one hour. Devin carries only
   `guardian-persona-delivery-read`; Cursor carries the platform-read
   capability set under its own attributable ServiceAccount identity.

Devin's delivery-read role can follow Flux sources and reconciliations, Kargo
freight and promotions, Flagger canaries, Kubernetes workload status, events,
and pod logs. It cannot read Secrets, RBAC, admission configuration, nodes,
storage, or Cilium state. It cannot exec, attach, port-forward, or mutate any
resource.

Cursor needs to close the production verification loop, so its one-hour
ServiceAccount is authority-equivalent to `platform-agent`: cluster read,
pod port-forward, and TokenRequests for the explicitly declared 15-minute
Mythra observer/operator capabilities. It still cannot read Secrets or perform
general writes, cannot renew its own bootstrap token, and is independently
attributable and revocable. Provider-specific fail-closed
ValidatingAdmissionPolicies preserve both boundaries even if RBAC is widened
later.

## Credential lifecycle

Authenticate locally with the unattended read rung, then mint the single
provider session credential without printing it:

```sh
aspect infra auth --persona=read
tools/ops/agent-cloud-token devin --output /dev/shm/guardian-devin-token
tools/ops/agent-cloud-token cursor --output /dev/shm/guardian-cursor-token
```

The read persona already carries the Cursor capability set and broader
authority than Devin, so minting either derived credential does not cross a
privilege boundary. RBAC pins the mint to the two exact ServiceAccounts and the
read persona's admission policy requires the Kubernetes audience and a lifetime
of no more than one hour. Store it only as a Devin `session_secret` or as a
Cursor Runtime Secret immediately before one cloud run, and remove any
temporary file immediately after injection. Reusing an expired environment
intentionally fails closed.

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

Cursor resolves the committed [`.cursor/environment.json`](../.cursor/environment.json)
before saved personal or team environments. Its install command prepares only
the pinned tools; its start command runs `scripts/agent-cloud-setup.sh` after
the VM boots so an expiring Kubernetes credential is never captured in a
Build. Devin runs setup explicitly. Use `scripts/agent-cloud-maintenance.sh`
only when resuming the same provider VM.

Setup materializes secrets to mode-0600 files and unsets them before invoking
repository-controlled build/tool code. It writes a named kubeconfig and links
it as the VM default only when no other default exists; it refuses to overwrite
another identity.

## Cursor write-basic on demand

Do not store a `platform-agent` password or refresh cache, a write-persona
password, or a write-persona token in Cursor Secrets or an environment
snapshot. Cursor's default JIT identity already provides the safe
platform-agent capability set without reusing the long-lived Keycloak account.

When a task genuinely needs routine repair outside the product-scoped Mythra
operator, start a fresh device flow inside that Cursor VM:

```sh
eval "$(scripts/bootstrap.sh path)"
aspect infra auth \
  --persona=write-basic \
  --install-path ~/.kube/guardian-cursor-write-basic
export KUBECONFIG="$HOME/.kube/guardian-cursor-write-basic"
kubectl auth whoami
```

The command prints a device URL that can be approved on any trusted machine.
Approve it as `platform-write-basic`; the agent must pause for the operator at
that point. This identity has no `offline_access`, expires with its Keycloak
session, and remains subject to the write-basic fail-closed admission policy.
Return to the provider identity with:

```sh
export KUBECONFIG="$HOME/.kube/guardian-cursor-cloud"
```

## Local development

Local Cursor and Devin continue to use the interactive browser/device flow:

```sh
aspect infra auth --persona=read
```

That path keeps the Keycloak refresh cache on the laptop and does not upload it
to either provider. Cursor Cloud uses its attributable, expiring
platform-read-equivalent identity; Devin uses delivery-read.

## Cloud proof contract

A Cursor task is complete only when Cursor reports success and the session
itself returns all of the following from the live cluster:

```sh
tools/ops/agent-cloud-tunnel status
kubectl auth whoami
kubectl get kustomizations.kustomize.toolkit.fluxcd.io -A
kubectl get stages.kargo.akuity.io,warehouses.kargo.akuity.io,promotions.kargo.akuity.io -A
kubectl get canaries.flagger.app -A
aspect mythra status
aspect mythra psql --query='SELECT 1'
! kubectl get secrets -A
! kubectl create namespace cloud-agent-write-denial --dry-run=server -o name
! kubectl exec -n cozy-fluxcd deploy/source-controller -- true
```

The expected identity is
`system:serviceaccount:tenant-root:guardian-cloud-agent-cursor`. `SELECT 1`
proves the session can derive and use the read-only Mythra observer without
revealing production journal data. Do not use `aspect mythra restart` as a
routine proof: it is intentionally available, but it recycles the live game
server.

For Devin, omit the two `aspect mythra` commands and retain the additional
negative port-forward check from its delivery-read proof; its expected identity is
`system:serviceaccount:tenant-root:guardian-cloud-agent-devin`.

## Durable federation follow-up

The one-hour TokenRequest path is the lowest-risk deployable path that does not
change Guardian's OpenBao initialization ceremony. The durable design removes
the local mint-and-inject step while preserving the same ServiceAccounts and
RBAC:

- Devin exchanges its native per-session, audience-bound workload OIDC token.
- Cursor exchanges a native five-minute, audience-bound OIDC token from
  `https://api.cursor.com`, restricted to the managed runtime and the complete
  `github.com/guardian-intelligence/guardian` repository set.
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
- <https://cursor.com/docs/cloud-agent/identity>
- <https://openbao.org/docs/auth/jwt/>
- <https://openbao.org/docs/plugins/>
