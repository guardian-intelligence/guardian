This is a Bazel polyglot monorepo and a free open-source repository housing all code including infrastructure and applications for Guardian Intelligence, a company founded by the user.

The management cluster runs Cozystack, variant `isp-full` with opt-in Gateway API.

`src/games/wake-up-mythra/README.md` is the canonical guide to WUM development setup and helpers: the one-command local stack (`aspect mythra dev up`), its edit loops and harnesses, and the `aspect mythra` operations verbs.

<operations_guidelines>
* GitOps: never maintain manual configuration, apply changes through IaC. Things that don't belong in git: Secrets, data, cluster/node state.
* Roll Forward, not Backward: avoid data corruption/security issues by root-causing issues and rolling the cluster forward to a known good state.
* Feature Flag client code, not services: it's impossible to reason about how a service will behave with a runtime configuration change. Prefer rolling restarts with a different OCI and direct traffic safely.
* Traces, Logs, and Metrics describe how the system works, not code. Commit/OCI hashes are useful for orienting yourself in time and space.
* Secure the Supply Chain - Pin dependencies, regularly accept security patches. Repo uses Renovate + GitHub App integration; Flux image automation moves first-party workload pins, and Kargo runs the postflight CLI release train. Trust tiers, doorbell semantics, the Actions-allowlist lockstep, and per-PR due diligence are in docs/dependency-management.md; policy is renovate.json5.
</operations_guidelines>

<development_loop>
Use this loop when pursuing autonomous development that requires a change to the repository's source code.

The loop is: worktree → change → PR/CI → merge → babysit convergence → babysit promotion/canary → babysit user signals → report to user. You are done when the change has converged and is healthy in the cluster.

Optional:

* Learn what development tooling exists with `aspect --help`
* Install tools if this is first time setup: `eval "$(scripts/bootstrap.sh path)" && aspect tools install && eval "$(aspect tools path)"`. Before cluster access, select the authentication route for the execution environment from `docs/agent-environment-authentication.md`. Tool shims installed by `aspect tools install` are available in `./.guardian/tools/bin`.


```sh
aspect infra watch                       # live Flux status with repo-pinned kubectl
aspect infra watch --mode=convergence    # ntfy stream: Flux convergence alerts only
aspect infra watch --mode=stream         # ntfy stream: all alerts, no cluster access
```

Step by step:
1. Branch in a git worktree off `origin/main` and make the planned edits. Run `aspect tidy && bazelisk build //... && bazelisk test //...` to format the repository, build its targets, and run local tests.
2. Open a PR via `gh` cli, monitor CI, perform adversarial review if needed, address blocking comments if any are posted, and then merge if all green.
3. Babysit Flux convergence, Kargo promotion, and Flagger deployment rollout. Tools:
    ```sh
    tools/ops/cluster-watch --status --until-ready --revision <merge-commit>
    aspect infra watch                       # live Flux status with repo-pinned kubectl
    aspect infra watch --mode=convergence    # ntfy stream: Flux convergence alerts only
    aspect infra watch --mode=stream         # ntfy stream: all alerts, no cluster access
    ```
4. If you're making a user-facing change to prod, monitor incoming traffic and query ClickHouse analytics to make sure users are having a good time. Also monitor Alerta during this time as most alerts take ~15 minutes to trigger, post Flux convergence.
5. Report task completion to the user with relevant metrics/logs/traces e.g. "LCP down for route /letters/<slug> from 3.4s to 3.2s based on last 30m of traffic to prod".

Common post-merge issues:
- `KustomizationNotApplied`
- Flux: `BuildFailed`, `denied by ValidatingAdmissionPolicy ...`, `HealthCheckFailed`, `dependency '...' is not ready`
- Kargo: runs the postflight CLI release train (nightly, RC, release); cluster workload pins move via Flux image automation (`src/infrastructure/deployments/guardian/imageops`). Check the pipeline under `src/infrastructure/deployments/guardian/promotion` if a CLI promotion fails.
- Flagger: A failed canary rolls back automatically and pages (Alerta) sometime later.
- Alerta: typically high signal, if there's unnecessary/unrelated noise, continue to monitor but assume it's your duty to fix noise unless you can make a strong case to flag to the user to fix separately. If it's a small fixup, even if unrelated, just tack on the fix instead of bothering the user. Default `cluster-watch` tails Alerta but alerts take ~15 minutes of sustained failure.

House rules:
- Do not use administration CLIs as a second control plane, use them for reads. Rely on Flux to converge the cluster after merge.
- If relevant to your task, clean up any hanging resources in the cluster post-merge.
</development_loop>

<cursor_cloud>
Cursor Cloud uses the repo-defined `.cursor/environment.json`, which runs
`scripts/agent-cloud-setup.sh` at session start. Its default
`guardian-cloud-agent-cursor` identity is platform-read-equivalent: cluster
read, port-forward, and the 15-minute product capabilities behind
`aspect mythra`, but no Secrets or general writes. Follow
`docs/cloud-agent-cluster-access.md` for the proof contract and the
device-approved `write-basic` escalation path. Never place a platform Keycloak
password, offline refresh cache, or write-persona token in a Cursor secret or
environment snapshot.
</cursor_cloud>

<web_workspace>
The pnpm workspace root is the repo root: `pnpm-workspace.yaml`, `package.json`,
`pnpm-lock.yaml`, and the dependency catalog all live beside `go.mod` and
`MODULE.bazel`. Workspace members are declared by glob, so a TypeScript package
lives with the Rust and Go it belongs to rather than in a language island.
Adding one is a glob entry plus a line in `WORKSPACE_PACKAGES` in `//BUILD.bazel`.

- Use `vp` (vite-plus), never raw `pnpm` — the system pnpm strips vp-specific
  fields out of `pnpm-lock.yaml`. If that happens, `git checkout pnpm-lock.yaml`
  and re-run `CI=true vp install`. Reach the package manager via `vp pm` if you
  genuinely need it.
- Don't use `corepack`.
- `vp install` / `vp dev` / `vp run ready` (lint + test + typecheck + build) all
  run from the repo root.
</web_workspace>

<products>
- Postflight - GitHub App, Blacksmith.sh but using QEMU warm pool, CRIU, on SEV-SNP hardware, ZFS for caching build artifacts and memory snapshots to create a "golden image" per repo. (In Progress)
- "Wake Up, Mythra!" (WUM) - Web game (native mobile apps planned) online cooperative city simulation tied to real-world dog parks.
</products>

Small note: `docs/TRIBAL_KNOWLEDGE.md` is useful to read for tricky issues.
