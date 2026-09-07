# CodeQL default-setup runner reconciliation

`tofu-guardian-codeql` uses the existing `tofu-runner` image, Flux artifact
fetch/digest verification, service account, and network policy. Its
`RECONCILER=codeql` branch never invokes OpenTofu. It reads
[`codeql-runner.json`](../bootstrap/guardian-github/codeql-runner.json) from the
verified artifact and mounts only `GITHUB_TOKEN` from `tofu-github`, through
`secretKeyRef`. It has no R2 backend key, state encryption passphrase, or
simulated-customer GitHub token. The declaration is outside `.github`, which
Flux's default sourceignore excludes.

The declaration owns only `state=configured`, `runner_type=labeled`, and
`runner_label=postflight-4vcpu-ubuntu24-turbo`. GitHub's
[default-setup API](https://docs.github.com/en/rest/code-scanning/code-scanning#update-a-code-scanning-default-setup-configuration)
accepts those routing fields independently. The required-language list is a
read assertion, never a PATCH field. The known coverage floor is `actions`,
`python`, and `rust`; the controller preserves any additional languages and
all unowned returned settings. The observed pre-migration settings were
`query_suite=default`, `threat_model=remote`, and `schedule=weekly`. Their
values remain GitHub-owned: they are compared before/after, not imposed.

## Plan, then separately activate

The committed CronJob is initially suspended and explicitly sets `MODE=plan`.
After the image pin contains the new reconciler, a reviewed Git change
unpauses plan observation. It GETs the live setup,
checks coverage, and logs `status=no-op` or `status=drift`; it cannot PATCH.
A plan success means observation succeeded, not that routing changed. API or
coverage errors fail the Job and remain visible through the existing
`tofu-*` failed-job alerts.

Keep the new CronJob suspended until Flux image automation pins an OCI
containing this controller. The old binary ignores `RECONCILER` and would
attempt OpenTofu initialization without backend credentials. After the OCI
converges, unpause in plan mode and verify its structured observation log.

Before applying, require a real Postflight Turbo CI canary with native GitHub
logs, VM-restore/identity proof, and disk-generation reuse. Require an actual `CodeQL routing observation` plan log from the promoted
image; a successful generic OpenTofu log is not that proof.

A separate reviewed Git change sets this CronJob's `MODE` to `apply`. Do not
PATCH repository settings by hand or create a second settings owner. A
fine-grained token needs repository Administration read for plans and write
for apply; observing an async validation also needs Actions read. Missing
permissions fail closed without logging the token or provider body. No
credential change belongs in the declaration or a workflow.

## Apply result contract

1. GET and assert existing language coverage. If the three owned fields
   match, report `no-op` without PATCH.
2. On mismatch in apply mode, PATCH exactly the three owned fields. Never
   include `languages`, query suite, threat model, schedule, or other settings.
3. A `202` is pending, not success. Use its numeric `run_id` at the fixed
   Guardian Actions API path; never follow a response-provided URL. Poll the
   validation for up to 40 minutes and require `completed/success`.
4. A `409` means another setup validation is in flight. GET once for
   observation, return `pending`, and do not PATCH again in that invocation.
   There are no pod retries; the next scheduled reconciliation starts with a
   fresh GET. Diagnose the outstanding GitHub validation rather than forcing
   another configuration.
5. After successful validation (or a synchronous `200`), GET again. Require
   the desired routing, the full prior language set, and unchanged unowned
   fields. Ignore only field ordering, language order, and `updated_at`.
   Drift, lost coverage, failed validation, or timeout never reports
   `converged`. Pending/failed jobs exit nonzero and retain their validation
   run ID in structured logs when known.

A matching read-only setup is not end-to-end proof that generated CodeQL
jobs used Turbo. After apply, inspect the actual GitHub validation run and
subsequent generated CodeQL jobs, their runner labels, conclusions, and
code-scanning coverage before declaring the migration complete. CodeQL
continues to use GitHub's generated default-setup workflow; this change does
not replace it with a repository-owned workflow or drop any language.

## Validation

`bazelisk test //src/infrastructure/cmd/tofu_runner:tofu_runner_test` covers
no-op and plan writes, exact PATCH ownership, async validation, pending
conflicts, failed or incomplete runs, coverage loss, and changes to unowned
settings. Those fake HTTP tests do not establish live GitHub token scope,
runner availability, or successful CodeQL execution.
