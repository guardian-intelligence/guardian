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

```
aspect infra auth --platform-agent
```

Daily driver: OIDC via the platform Keycloak. Opens a browser once, then
refreshes headlessly (offline token; 30-day idle window that resets on every
use; revocable in Keycloak). Bootstraps everything it needs from the repo —
pinned kubelogin, committed cluster CA — and selects the
`platform-agent@guardian-mgmt` kubectl context.

```
aspect infra auth --platform-admin --reason "<why>"
```

Breakglass: escalated x509 `system:masters` credentials minted from the
Talos custody bundle (the gitignored operator state in
`src/infrastructure/talm/` — `secrets.yaml` + `talm.key`). Every use is
audit-logged: the reason pages the operations ntfy topic and is appended to
the in-cluster `breakglass-audit` ledger and the local custody log.
