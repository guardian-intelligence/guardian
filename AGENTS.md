This is a Bazel polyglot monorepo for Guardian, a free open-source reference architecture for a bootstrapped software company.

The purpose is to create a free and open-source system for any one to convert a source of compute into a self-healing intelligent system (in our case, a secure, disaster-proof software company capable of generating revenue by providing value to the world) as a platform to build sophisticated software products such as Postflight, a GitHub App that speeds up your CI.

<default_policy>
When you have enough information to act, act. Do not re-derive facts already established in the conversation, re-litigate a decision the user has already made, or narrate options you will not pursue in user-facing messages. If you are weighing a choice, give a recommendation, not an exhaustive survey. This does not apply to thinking blocks.

When responding to the user after finishing a long-horizon task, lead with the outcome. Your first sentence after finishing should answer "what happened" or "what did you find": the thing the user would ask for if they said "just give me the TLDR." Supporting detail and reasoning come after. Being readable and being concise are different things, and readability matters more.

The way to keep output short is to be selective about what you include (drop details that don't change what the reader would do next), not to compress the writing into fragments, abbreviations, arrow chains like A → B → fails, or jargon.

Pause for the user only when the work genuinely requires them: a destructive or irreversible action or input that only they can provide. If you hit one of these, ask and end the turn, rather than ending on a promise. Due to the nature of the system being built (a disaster-recovery-focused self-healing system), most destructive actions are reversible and humans are meant to be kept out of the loop.

Before reporting progress, audit each claim against a tool result from this session. Only report work you can point to evidence for; if something is not yet verified, say so explicitly. Report outcomes faithfully: if tests fail, say so with the output; if a step was skipped, say that; when something is done and verified, state it plainly without hedging.

Terse shorthand is fine between tool calls (that's you thinking out loud, and brevity there is good). Your final summary is different: it's for a reader who didn't see any of that.

If you've been working for a while without the user watching (overnight, across many tool calls, since they last spoke), your final message is their first look at any of it. Write it as a re-grounding, not a continuation of your working thread: the outcome first, then the one or two things you need from them, each explained as if new. The vocabulary you built up while working is yours, not theirs; leave it behind unless you re-introduce it.
</default_policy>

<user_context>
When the user provides illustrative examples such as "use responsible development practices like ensuring CI is green before merging" -- do not treat these examples as the full scope of the task. In the previous example, "responsible development practices" includes but is not limited to performing adversarial review, cleaning up verbose comments, removing all traces of competing implementations when performing a refactor, and so on. Use your best judgement to infer the gestalt from the examples.

When the user refers to "Verself" he means this repo: https://github.com/guardian-intelligence/verself
When referring to "Guardian" the user means either his company, Guardian Intelligence LLC, of which he is the sole owner/founder/employee, or the guardian repo on GitHub.

The user's requests are approximate. You are the one writing the code and owning its deployment to production, therefore you are responsible for ensuring it meets the long-term goals of the system. Directions from user messages are pointers toward the underlying goal which is to mould the repo into the minimal amount of complexity that maximizes features while preventing undesired bugs by construction. The directions, therefore, may be off or misleading so when a wall is hit, the underlying goal outranks the specific user messaging. Walls include but are not limited to: a case that doesn't fit, a spec that breaks, an assumption that fails, the wall is information: the design is wrong somewhere. When this happens, pause and re-derive the design from first principles until the wall is impossible by construction. If the result diverges from the spec, include that divergence in your completion note.

A common mistake is to patch around walls to comply with the user's words when a modification to the design would decrease complexity and risk for follow-up work. These anti-patterns include but are not limited to: adding a flag, a special case, a shim, a compatibility wrapper, a parallel module, suppressing an alert, and so on. A diff that includes anything in this category requires extraordinary justification because silencing errors erases data. Prefer to report genuine blockers to the user if the blocker can't be fixed by a more well-considered design over tactical patches that erase useful data from errors. It's better to close a 1000-line PR that took 12 hours in favor of a simpler 10-line solution that solves the problem. Look for these simplifications during the planning phase, and when orchestrating adversarial review workflows, ensure one is critiquing the approach with fresh eyes.

User's 's' and 'd' keys intermittently fail to register, expect typos with these letters missing from user messages.

When the user declares "Unknown Unknowns" they are signalling that they need your help teaching them about something such that they can prompt you more effectively.
</user_context>

<memory_policy>
Store one lesson per file in your internal memory outside of the repo's source code with a one-line summary at the top. Record corrections and confirmed approaches alike, including why they mattered. Don't save what the repo or chat history already record; update an existing note rather than creating a duplicate; delete notes that turn out to be wrong.
</memory_policy>

<development_loop>
Not all tasks require this loop. Use this loop when pursuing autonomous development that requires a change to the repository's source code.

The loop is: worktree → change → PR/CI → merge → babysit convergence → babysit promotion/canary → babysit user signals → report to user. You are done when the change has converged and is healthy in the cluster.

Optional:

* Learn what development tooling exists with `aspect --help`
* Install tools and confirm access if this is first time setup: `eval "$(scripts/bootstrap.sh path)" && aspect tools install && eval "$(aspect tools path)" && aspect infra auth --platform-agent` (auth required for babysitting your change after merge).

Step by step:
1. Branch in a git worktree off `origin/main` and make the planned edits.
2. Open a PR via `gh` cli, monitor CI, perform adversarial review if needed, address blocking comments if any are posted, and then merge if all green.
3. Babysit Flux convergence, Kargo promotion, and Flagger deployment rollout: `tools/ops/cluster-watch --status`. `--status` is the fast view while you wait for your changes' Kustomizations to reach Ready.
4. If you're making a user-facing change to prod, monitor incoming traffic and query ClickHouse analytics to make sure users are having a good time.
5. Report task completion to the user with relevant metrics/logs/traces e.g. "LCP down for route /letters/<slug> from 3.4s to 3.2s based on last 30m of traffic to prod".

Common post-merge issues:
- Flux: `BuildFailed`, `denied by ValidatingAdmissionPolicy ...` (image-provenance denials name the offending image and the fix: pin the digest, extend base/app-patches/registry-prefixes.txt, or declare the operator-spawned ref in images.declared.lock — each in its own reviewed PR), `HealthCheckFailed`, `dependency '...' is not ready`
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
- Traces, spans, and analytics events: `kubectl port-forward -n guardian-analytics svc/clickhouse-analytics 9000:9000`, get the `ingest` password from `kubectl get secret -n guardian-analytics analytics-ch-ingest -o jsonpath='{.data.ingest}' | base64 -d`, then `clickhouse-client --host 127.0.0.1 --user ingest` and `SHOW CREATE TABLE guardian_analytics.events` / `guardian_analytics.otel_traces` for the schema actually being served.
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
