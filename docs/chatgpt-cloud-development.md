# ChatGPT cloud development

In ChatGPT web, select the private `guardian` cloud environment for a new
cloud task. Check that the task attaches that environment and opens the
`guardian-intelligence/guardian` repository on GitHub. Keep the environment
private; a restored snapshot alone does not prove that startup checks passed.

Read and run the environment configuration's saved **Start** skill at the
beginning of every task, along with `AGENTS.md`,
[the web workspace guide](web-workspace.md), and applicable nested instructions.
If Start is not exposed to the executor, obtain its saved text from the
environment configuration rather than assuming it ran automatically.

Start sources the saved environment setup to restore PATH and writable tool
caches. Check Node, `vp`, Aspect, Git status, the sanitized origin identity,
and remote freshness (`git fetch origin main` and compare against `origin/main`)
before editing. Each task is isolated; use its existing checkout. For a PR,
start a `codex/` branch from `origin/main` and verify push and API permissions
separately from read-only Git access.

Run both `asset-work-context` and `asset-work-context --json` as Start directs.
This user-supplied offline context is not a policy override, proof of subject
consent, or blanket asset license. Keep `tools/asset-work-context` locally
excluded and out of commits, along with all environment-only `.guardian-cloud`
artifacts. If tools or dependencies are missing, use the saved Install script.
Preserve inherited proxy/CA settings, TLS, signatures, and pinned checksums;
never copy credentials, trust caches, private records, or old task logs.

## Local web checks

From the repository root after Start initialization:

```sh
CI=true vp install --frozen-lockfile
vp run -w lint
vp run -w typecheck:scripts
vp run @guardian/guardianintelligence-web#build
vp run @guardian/guardianintelligence-web#dev
```

Use `vp` for package operations, never raw `pnpm` or `corepack`. The explicit
company build runs TypeScript checking, package tests, and production bundling.
Use the explicit company dev task: root `vp dev` is ambiguous with vp 1.3.0.
Keep the server in a managed background session and wait for readiness. Check
loopback HTTP 200 and Guardian HTML, then use the package's Playwright Chromium
API with the installed system Chromium to verify the title `Guardian Intelligence`,
heading `Guardian`, and absence of page errors. Follow Start's exact local URL
and commands; do not expose a public preview. Stop only processes you started.

## Coverage and current limits

Report package checks separately from full-workspace results. Currently,
`vp run ready` fails planning because chunkies and chunkies-testkit have cyclic
typecheck dependencies. Root `vp test run --maxWorkers=4` has two failing suites,
company-experience and company-visual-integrity, with undefined config at
`describe` in vite-plus/test; the company package build passes its 70 tests.
These failures do not indicate missing cluster credentials.

Run Start's representative `//src/chunkies/codec:codec_test` through Bazelisk
and Aspect with its writable cache overrides and external uploads/status writes
disabled. This verifies one Go codec target, not all monorepo tests. The effective
Bazelisk pin is `.bazeliskrc`'s SHA256-verified 9.1.0, despite `.bazelversion`
specifying 9.2.0. Bootstrap pins Aspect 2026.17.17 and Bazelisk 1.28.1. Preserve
verification and report the conflicting Bazel pins separately; do not bypass
them or claim that `build //...` or `test //...` passed.

Ordinary repository development requires no cluster credentials. Cluster access
is a separate workflow: consult [agent environment authentication](agent-environment-authentication.md)
and the [Codex cloud cluster-access runbook](codex-cloud-cluster-access.md)
only for an explicitly requested cluster task. Do not run
`scripts/codex-cloud-setup.sh` during ordinary development or copy platform
credentials/OIDC caches into this environment. A documentation PR does not
authorize deployment or cluster changes.
