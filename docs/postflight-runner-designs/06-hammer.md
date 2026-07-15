# 06 — Hammer harness

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

The repeatable proof. One command dispatches real Next.js CI jobs against
the tracer host in configurable patterns, watches them to conclusion, and
asserts the system's books balance afterward. This is also where every
number the design promised gets measured instead of estimated.

## Shape

`src/tools/postflight-hammer/` — a Go CLI (repo-standard, Bazel-built) with
three subcommands:

- `dispatch` — fire `workflow_dispatch` on `postflight-nextjs-demo` via the
  App installation token. Patterns: `burst N` (all at once), `sustained N/m`
  (steady rate), `churn` (dispatch, cancel at a random point, re-run),
  `restart` (dispatch load, then `systemctl restart hostd` mid-flight).
- `watch` — poll GitHub (runs/jobs, attempt-specific conclusions) and the
  controlplane DB (demand/lease rows and timestamps) until every dispatched
  run is terminal.
- `report` — join both sources and print the scoreboard.

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
5. Warm-vs-cold checkout: after the first green run of a scope, subsequent
   runs fetch deltas only (bundle-server bytes served per lease attests it).
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
