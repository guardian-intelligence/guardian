# 06 — Hammer harness

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

The repeatable proof. One command dispatches real Next.js CI jobs against
the tracer host in configurable patterns, watches them to conclusion, and
asserts the system's books balance afterward. This is also where every
number the design promised gets measured instead of estimated.

## Shape

`src/tools/postflight-hammer/` — a Go CLI (repo-standard, Bazel-built) with
four subcommands:

- `dispatch` — fire `workflow_dispatch` on `postflight-nextjs-demo` via the
  operator token. Repeatable `-input key=value` flags select benchmark lanes
  and cohorts and are retained in the state artifact. Patterns: `burst N`
  (all at once), `serial N` (wait for each run, and optionally its workspace
  generation promotion, before firing the next), `sustained N/m`
  (steady rate), `churn` (dispatch, cancel at a random point, re-run),
  `restart` (dispatch load, then `systemctl restart hostd` mid-flight).
- `watch` — poll GitHub (runs/jobs, attempt-specific conclusions) and the
  controlplane DB (demand/lease rows and timestamps) until every dispatched
  run is terminal.
- `report` — join both sources and print the scoreboard.
- `validate-rendezvous` — validate the append-only host/guest/runner JSONL
  trace for one assignment-first job.

## Assignment-first rendezvous

The warm VM and its generic ephemeral runner exist before customer identity
is known. The conformance trace uses a collector-assigned logical sequence;
monotonic timestamps are compared only within one source, never between the
host, guest, and GitHub.

1. `pool_ready`: the QEMU VM is running and its generic runner is listening.
   It names only the listener lease, runner, and VM; it carries no run,
   execution lease, or customer volume.
2. `assignment_observed`: GitHub's actual numeric job id, run attempt, and
   selected runner name are observed. The trace now names the selected
   listener lease and the job-owned execution lease separately.
3. `job_hook_blocked`: the synchronous job-start hook holds the runner before
   any customer step.
4. `job_identity_reported`: the hook reports runner, job, and repository
   identity to the host.
5. `generation_resolved`: the control plane resolves one immutable workspace,
   toolchain, data, and optional memory snapshot tuple for that exact job.
6. `rendezvous_bound`: the host atomically hot-binds the generation set's
   workspace and optional toolchain, data, and memory zvols to that exact
   QEMU VM.
7. `mounts_ready` and `clock_checked`: guestd mounts and verifies every
   resolved device by its stable serial, then records a bounded host/guest
   realtime sample after memory restore when applicable.
8. `job_hook_released`: only now may the Actions runner execute the job.

The validator rejects a changed job, run attempt, runner, listener lease,
execution lease, VM, or generation-set identity,
a pool VM that already knows customer identity or carries customer volumes,
a workspace dataset that does not belong to the routed execution lease,
a bound or mounted tuple that differs from the resolved snapshots, a missing
workspace, a memory snapshot without its workspace, duplicate volume roles,
and hook release before mounts and post-restore clock evidence. Deterministic
assignment means reacting to GitHub's observed runner mapping, including
same-label listener displacement; it does not mean predicting which JIT
registration GitHub will select.

## Snapshot discipline

Snapshot creation is a candidate protocol, separate from promotion:

- normal PR and workflow-dispatch runs record `decision=skip`; they do not
  manufacture product goldens;
- a candidate may be generated only for a trusted protected-main run or an
  explicit benchmark seed, after the runner exits successfully;
- Actions runner memory is prohibited. Only explicitly allowlisted build
  daemons may be captured;
- durable filesystems are quiesced before their snapshots and the process
  image and every volume in the bound generation set share one manifest
  identity;
- the candidate is promoted only after GitHub reports attempt-scoped success.
  Every ambiguity or non-success conclusion discards it.

Clock evidence brackets one guest realtime sample with host samples. The
report uses midpoint offset plus half the sampling round-trip as a
conservative skew bound. All latency measurements remain monotonic. A skew
bound violation is a concern with raw evidence, not a negative duration
silently accepted after restore.

Every trace fingerprints QEMU, the host kernel, Ubuntu image, machine type,
CPU, and CRIU when memory is restored. QEMU/Ubuntu/CRIU defects are recorded
as `platform_bug` concerns with the affected fingerprint. A workaround does
not turn a workload into a failure.

## Result vocabulary

- `PASS`: valid evidence and the workload completed.
- `CONCERN`: valid evidence with a performance or recoverable operational
  issue, including losing to another provider on temporary hardware.
- `INVALID`: setup, workload, assignment, or evidence was not valid enough to
  compare. Missing binaries and broken benchmark commands land here.
- `FAIL`: reserved for evidence that durable volumes are fundamentally
  unsound, or a workload remains categorically unsupported after both the
  traditional container and durable-toolchain paths were exercised.
- `SKIP`: an optional assertion was not exercised; a full battery should have
  none.

## Assertions (a failed assertion fails the run)

1. Every non-cancelled dispatch reaches `success` on GitHub, and its full
   build-and-test logs are retrievable (`gh run view --log` non-empty for
   every step — the observability requirement is asserted, not eyeballed).
2. Slot accounting returns exactly to baseline (`host_slots` reserved/used
   counters, warm pool refilled).
3. No leaks: zero orphan zvols under `ws/`, zero VM state dirs, zero
   non-terminal leases/demands older than the deadline horizon.
4. Deadlines fire: churn-cancelled jobs reach a terminal demand state within
   bound (today that bound is the 30m ready deadline — the recorded
   cancellation-propagation follow-up; the assertion encodes whatever the
   current contract is, so tightening it later is a one-line change).
5. Warm-vs-cold checkout: after the first green run of a scope, a rerun of
   an already-fetched SHA serves zero bundle bytes, and a new-SHA run
   serves exactly one single-commit closure over the host-local link
   (bundle-server bytes-served + cache-hit counters per lease attest it);
   GitHub egress is only the mirror's incremental fetch.
6. Generation lifecycle: exactly one `current` per exercised scope, losers
   `retained`, every `sealed` candidate resolved (`committed`/`discarded`),
   invariants 1–5 of 04 hold in the final DB state.

## Measurements (the report)

- **Pickup**: webhook received → job `in_progress` on GitHub, p50/p90/p100,
  split by segment (ingest, claim, JIT, hostd pickup, runner listening,
  GitHub assignment) from lease/demand timestamps. Baseline to beat:
  composed ~9.3s today, 2.0s GitHub floor with warm listeners.
- **Exec**: job duration vs the GitHub-hosted twin workflow (baseline: 25s
  vs 37s p50).
- **Seal**: runner exit → snapshot done (quiesce included). Design claim:
  low tens of ms plus one sync round-trip.
- **NVMe**: per generation at seal — `used`, `written`, `logicalused`,
  `compressratio` (recorded by hostd, 04). The report graphs growth per
  scope across N runs: clone divergence (`written`) is the marginal cost of
  a run, and the curve validates sparse overcommit and feeds the eviction
  policy. This is the measurement that decides whether job-shape scope dims
  (more lineages) are affordable — the open cost question from the scope-key
  ruling.

## Runbook

Documented in the tool's `--help`: prerequisites (controlplane enabled,
hostd synced, image templated), a 5-dispatch smoke, the full battery
(≥50 across all patterns), and how to read the report. The battery is
re-runnable at will — it is the standing answer to "is the system still
healthy after this change."
