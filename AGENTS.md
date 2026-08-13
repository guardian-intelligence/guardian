This is a Bazel polyglot monorepo and a free open-source repository housing all code including infrastructure and applications for Guardian Intelligence, a company founded by the user.

Read `docs/TRIBAL_KNOWLEDGE.md` before making changes to this repository.

The management cluster runs Cozystack, variant `isp-full` with opt-in Gateway API.

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

<coding_guidelines>
* Do not use GitHub Actions workflow YAMLs as a second control plane. Prefer to move tasks including but not limited to: generating Preview Deployments, generating/signing images, scheduled jobs, and so on, into the source code, rather than hairpinning cluster administration through GitHub.
</coding_guidelines>

Product Surfaces:

- Postflight - GitHub App, Blacksmith.sh but using QEMU warm pool, CRIU, on SEV-SNP hardware, ZFS for caching build artifacts and memory snapshots to create a "golden image" per repo. (In Progress)
- PrivateCut - rumi.engineering, browser-native video clipper: up to a minute of any video at the best quality that fits under a hard user-selected size cap — 4 MB by default, up to 100 MB (mediabunny in a worker, measured-size acceptance gate, no upload). (In Progress)
- "Wake Up, Mythra!" (WUM) - Web game (native mobile apps planned) online cooperative city simulation tied to real-world dog parks. Rust -> `wazero` core + client crate for camera, device bindings, interpolation, reconnect logic. Go service.
