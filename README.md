# guardian

Guardian runs on a Cozystack-native management cluster. Development is done via GitOps.

## Quickstart

Run `eval "$(scripts/bootstrap.sh path)" && aspect tools install && eval "$(aspect tools path)"`
to install the pinned bootstrap toolchain plus repo-pinned CLIs and build tools,
`aspect build`, `aspect lint`, `aspect test`, and `aspect tidy` to build, lint,
test, and format the repo (fast with cache), and `aspect --help` /
`aspect <task> --help` to view development tasks and their options.

Deeper reading: `AGENTS.md` (conventions and the durable command surface), the
web frontend dev loop in `src/products/viteplus-monorepo/README.md`, the
runbooks in `src/infrastructure/runbooks/`, and the design docs in `docs/`.

Generated Talm secrets, rendered node configs, kubeconfigs, and local operator
state stay out of Git.

## How to authenticate to your cluster

Day-to-day cluster access is OIDC against the platform Keycloak (realm `cozy`
at `keycloak.<your-host>`); the Talos-minted x509 admin kubeconfig is the
breakglass path, not the daily driver. Two identities ship in
`src/infrastructure/base/cozystack/platform-admins.yaml`, both in the
`cozystack-cluster-admin` group (cluster-admin via the apiserver's `groups`
claim); their passwords live in OpenBao under
`kv/guardian/guardian-mgmt/tenant-root/platform-admins` and are seeded from
the custody env by the importer plan.

**Humans (`platform-admin`)** — the repo pins [int128/kubelogin](https://github.com/int128/kubelogin)
as `kubectl-oidc_login` (installed with the rest of the toolchain by
`aspect tools install`; nothing external to install). Add an exec user to
your kubeconfig and point a context at it:

```yaml
users:
  - name: oidc
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1beta1
        command: kubectl-oidc_login
        args:
          - get-token
          - --oidc-issuer-url=https://keycloak.guardianintelligence.org/realms/cozy
          - --oidc-client-id=kubernetes
        interactiveMode: IfAvailable
```

The first `kubectl` opens a browser once; sessions then refresh silently
(24h idle / 7d max — upstream chart defaults, not overridable in values).

**Agents (`platform-agent`)** — same exec user plus
`--oidc-extra-scope=offline_access` and a dedicated
`--token-cache-dir`. Offline tokens survive 30 days idle, reset on every
use, and are individually revocable in Keycloak (Sessions → Offline). The
`offline_access` realm role on the user is what authorizes this; without it
Keycloak refuses the code exchange.

**Breakglass (x509)** — `aspect infra kubeconfig --install` re-mints the
`CN=admin, O=system:masters` kubeconfig from Talos custody
(`src/infrastructure/talm/`, gitignored). It is Keycloak-independent — the
cold-boot runbook depends on it — and cannot be revoked by RBAC, so treat
the custody bundle as the root of trust it is: every use is a deliberate,
auditable event, and the file should not live on daily-driver machines.

The Keycloak admin console is never publicly routed (`/admin` and
`/realms/master` 503 at the origin by design — see
`base/cozystack/keycloak-admin-guard.yaml`); reach it via
`kubectl -n cozy-keycloak port-forward svc/keycloak-http 8080:8080` with the
master credentials in `Secret/keycloak-credentials`.
