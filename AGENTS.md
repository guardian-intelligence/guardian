This is a Bazel polyglot monorepo for Guardian, a free open-source reference architecture for a bootstrapped software company.

The purpose is to create a free and open-source system for any one to convert a source of compute into a self-healing intelligent system (in our case, a secure, disaster-proof software company capable of generating revenue by providing value to the world) as a platform to build sophisticated software products such as Postflight, a GitHub App that speeds up your CI.

Read `docs/TRIBAL_KNOWLEDGE.md` before making changes to this repository.

The management cluster runs Cozystack, variant `isp-full` with opt-in Gateway API.

Reference https://github.com/guardian-intelligence/verself/tree/main/docs for information relating to:

* Zvol architecture
* "Golden Image" pattern
* Snapshot/restore (Verself uses just-in-time firecracker, this repo uses QEMU warmpool on SEV-SNP-compatible hardware)

<operations_guidelines>
* GitOps: never maintain manual configuration, apply changes through IaC. Things that don't belong in git: Secrets, data, cluster/node state.
* Roll Forward, not Backward: avoid data corruption/security issues by root-causing issues and rolling the cluster forward to a known good state.
* Feature Flag client code, not services: it's impossible to reason about how a service will behave with a runtime configuration change. Prefer rolling restarts with a different OCI and direct traffic safely.
* Traces, Logs, and Metrics describe how the system works, not code. Commit/OCI hashes are useful for orienting yourself in time and space.
* Secure the Supply Chain - Pin dependencies, regularly accept security patches. Repo uses Renovate + GitHub App integration, Kargo proposes rendered stage images. Trust tiers, doorbell semantics, the Actions-allowlist lockstep, and per-PR due diligence are in docs/dependency-management.md; policy is renovate.json5.
</operations_guidelines>

<development_loop>
Not all tasks require this loop. Use this loop when pursuing autonomous development that requires a change to the repository's source code.

The loop is: worktree → change → PR/CI → merge → babysit convergence → babysit promotion/canary → babysit user signals → report to user. You are done when the change has converged and is healthy in the cluster.

Optional:

* Learn what development tooling exists with `aspect --help`
* Install tools and confirm access if this is first time setup: `eval "$(scripts/bootstrap.sh path)" && aspect tools install && eval "$(aspect tools path)" && aspect infra auth --platform-agent` (auth required for babysitting your change after merge). Tool shims installed by `aspect tools install` are available in `./.guardian/tools/bin`.


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
- Kargo: manages promotions of pushed code to edge (every change), nightly, RC, and release builds. Check the Kargo configuration for the distributable that failed promotion.
- Flagger: A failed canary rolls back automatically and pages (Alerta) sometime later.
- Alerta: typically high signal, if there's unnecessary/unrelated noise, continue to monitor but assume it's your duty to fix noise unless you can make a strong case to flag to the user to fix separately. If it's a small fixup, even if unrelated, just tack on the fix instead of bothering the user. Default `cluster-watch` tails Alerta but alerts take ~15 minutes of sustained failure.

House rules:
- Do not use administration CLIs as a second control plane, use them for reads. Rely on Flux to converge the cluster after merge.
- If relevant to your task, clean up any hanging resources post-merge. Write access is audit logged and pages a human. Write access requires `aspect infra auth --platform-admin --reason "<why>"`.
</development_loop>

<observability>
- Logs: `kubectl port-forward -n tenant-root svc/vlselect-generic 9471:9471`, then LogsQL via `curl 127.0.0.1:9471/select/logsql/query --data-urlencode 'query=...'`.
- Metrics: `kubectl port-forward -n tenant-root svc/vmselect-shortterm 8481:8481`, then PromQL via `curl 127.0.0.1:8481/select/0/prometheus/api/v1/query --data-urlencode 'query=...'`.
- Traces, spans, and analytics events: `kubectl port-forward -n tenant-root svc/chendpoint-clickhouse-analytics 9000:9000`, get the `ingest` password from `kubectl get secret -n guardian-analytics analytics-ch-ingest -o jsonpath='{.data.ingest}' | base64 -d`, then `clickhouse-client --host 127.0.0.1 --user ingest` and `SHOW CREATE TABLE guardian_analytics.events` / `guardian_analytics.otel_traces` for the schema actually being served.
- Schema source: `src/infrastructure/deployments/analytics/system/{ddl-configmap.yaml,traces-configmap.yaml}`.
- Dropped network flows: the Cilium agents export every `DROPPED`/`ERROR` flow as JSON on stdout, so they land in VictoriaLogs with the rest of the container logs. `hubble_drop_total` says a namespace is being denied; this says which peer, port, and policy. LogsQL: `kubernetes_container_name:cilium-agent AND _msg:POLICY_DENIED | unpack_json | keep _time, source, destination, IP, l4, drop_reason_desc, egress_denied_by`. There is no Hubble relay to query — see `src/infrastructure/base/platform-patches/cozystack-networking-hubble.yaml` for why.
</observability>

<coding_guidelines>
* Improvements and refactors should leave no trace that the old approach ever existed unless someone spelunks through git history. This means that comments should not reference the previous approach nor should any compatibility shims be provided. E.g. if migrating from Cozystack v1.4.0 -> v1.5.0 avoid comments like "this is required for 1.5.0 whereas 1.4.0 did XYZ".
* Only add comments for genuinely complex workarounds for bugs or surprising deviations from best practices. Clean up comments that don't adhere to this rule.
* Do not use GitHub Actions workflow YAMLs as a second control plane. Prefer to move tasks including but not limited to: generating Preview Deployments, generating/signing images, scheduled jobs, and so on, into the source code, rather than hairpinning cluster administration through GitHub.
</coding_guidelines>

Product Surfaces:

- Postflight - GitHub App, Blacksmith.sh but using QEMU warm pool, CRIU, on SEV-SNP hardware, ZFS for caching build artifacts and memory snapshots to create a "golden image" per repo. (In Progress)
- Shortty - rumi.engineering, browser-native video clipper: up to a minute of any video at the best quality that fits under a hard 4 MB limit (mediabunny in a worker, measured-size acceptance gate, no upload). (In Progress)
