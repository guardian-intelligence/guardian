This is a Bazel polyglot monorepo for Guardian, a free open-source reference architecture for a bootstrapped software company.

The purpose is to create a free and open-source system for any one to convert a source of compute into a self-healing intelligent system (in our case, a secure, disaster-proof software company capable of generating revenue by providing value to the world) as a platform to build sophisticated software products such as Postflight, a GitHub App that speeds up your CI.

Read `docs/TRIBAL_KNOWLEDGE.md` before making changes to this repository.

The management cluster runs Cozystack, variant `isp-full` with opt-in Gateway API.

Reference https://github.com/guardian-intelligence/verself/tree/main/docs for information relating to:

* Zvol architecture
* "Golden Image" pattern
* Snapshot/restore (Verself uses just-in-time firecracker, this repo uses QEMU warmpool on SEV-SNP-compatible hardware)

<development_loop>
Not all tasks require this loop. Use this loop when pursuing autonomous development that requires a change to the repository's source code.

The loop is: worktree → change → PR/CI → merge → babysit convergence → babysit promotion/canary → babysit user signals → report to user. You are done when the change has converged and is healthy in the cluster.

Optional:

* Learn what development tooling exists with `aspect --help`
* Install tools and confirm access if this is first time setup: `eval "$(scripts/bootstrap.sh path)" && aspect tools install && eval "$(aspect tools path)" && aspect infra auth --platform-agent` (auth required for babysitting your change after merge). Tool shims installed by `aspect tools install` are available in `./.guardian/tools/bin`.

Step by step:
1. Branch in a git worktree off `origin/main` and make the planned edits. Run `aspect tidy && bazelisk build //... && bazelisk test //...` to format the repository, build its targets, and run local tests.
2. Open a PR via `gh` cli, monitor CI, perform adversarial review if needed, address blocking comments if any are posted, and then merge if all green.
3. Babysit Flux convergence, Kargo promotion, and Flagger deployment rollout: `tools/ops/cluster-watch --status --until-ready --revision <merge-commit>`.
4. If you're making a user-facing change to prod, monitor incoming traffic and query ClickHouse analytics to make sure users are having a good time. Also monitor Alerta during this time as most alerts take ~15 minutes to trigger, post Flux convergence.
5. Report task completion to the user with relevant metrics/logs/traces e.g. "LCP down for route /letters/<slug> from 3.4s to 3.2s based on last 30m of traffic to prod".

Common post-merge issues:
- Flux: `BuildFailed`, `denied by ValidatingAdmissionPolicy ...` (image-provenance denials name the offending image and the fix: pin the digest, extend base/admission/registry-prefixes.txt, or declare the operator-spawned ref in images.declared.lock — each in its own reviewed PR), `HealthCheckFailed`, `dependency '...' is not ready`
- Kargo (image changes only): promotions are done by Kargo GitHub app bot commits.
- Flagger: A failed canary rolls back automatically and pages.
- Alerta: typically high signal, if there's unnecessary/unrelated noise, continue to monitor but assume it's your duty to fix noise unless you can make a strong case to flag to the user to fix separately. If it's a small fixup, even if unrelated, just tack on the fix instead of bothering the user. Default `cluster-watch` tails Alerta but alerts take ~15 minutes of sustained failure.

House rules:
- Do not use CLI commands as a second control plane. Rely on Flux to converge the cluster after merge.
- If relevant to your task, clean up any hanging resources post-merge. Write access is audit logged and pages a human. Write access requires `aspect infra auth --platform-admin --reason "<why>"`.
</development_loop>

<observability>
- Logs: `kubectl port-forward -n tenant-root svc/vlselect-generic 9471:9471`, then LogsQL via `curl 127.0.0.1:9471/select/logsql/query --data-urlencode 'query=...'`.
- Metrics: `kubectl port-forward -n tenant-root svc/vmselect-shortterm 8481:8481`, then PromQL via `curl 127.0.0.1:8481/select/0/prometheus/api/v1/query --data-urlencode 'query=...'`.
- Traces, spans, and analytics events: `kubectl port-forward -n tenant-root svc/chendpoint-clickhouse-analytics 9000:9000`, get the `ingest` password from `kubectl get secret -n guardian-analytics analytics-ch-ingest -o jsonpath='{.data.ingest}' | base64 -d`, then `clickhouse-client --host 127.0.0.1 --user ingest` and `SHOW CREATE TABLE guardian_analytics.events` / `guardian_analytics.otel_traces` for the schema actually being served.
- Schema source: `src/infrastructure/analytics/events-table.sql` (canonical events DDL) and `src/infrastructure/deployments/analytics/system/{ddl-configmap.yaml,traces-configmap.yaml}` (what's actually rendered and applied in-cluster).
</observability>

<dependency_management>
One proposer per pin: Renovate proposes source-plane pins, Kargo proposes rendered stage images — never hand-roll either's PRs. Trust tiers, doorbell semantics, the Actions-allowlist lockstep, and per-PR due diligence are in docs/dependency-management.md; policy is renovate.json5.
</dependency_management>

<coding_guidelines>
* Improvements and refactors should leave no trace that the old approach ever existed unless someone spelunks through git history. This means that comments should not reference the previous approach nor should any compatibility shims be provided. E.g. if migrating from Cozystack v1.4.0 -> v1.5.0 avoid comments like "this is required for 1.5.0 whereas 1.4.0 did XYZ".
* Only add comments for genuinely complex workarounds for bugs or surprising deviations from best practices. Clean up comments that don't adhere to this rule.
* Do not use GitHub Actions workflow YAMLs as a second control plane. Prefer to move tasks including but not limited to: generating Preview Deployments, generating/signing images, scheduled jobs, and so on, into the source code, rather than hairpinning cluster administration through GitHub.
</coding_guidelines>

Planned Product Surfaces:

- Postflight - GitHub App (20x faster CI than GitHub Actions; running untrusted customer CI requires TEE on the rs4 workload nodes first). (In Progress)
- Empire - Software Company from an API call or web surface; host come-up tooling only prepares machines for the management cluster. (Not Yet Implemented)
