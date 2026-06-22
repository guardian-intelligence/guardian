# Infrastructure Verification Reports

This directory holds checked-in evidence for new management-cluster
infrastructure components.

Use `templates/component-operational-report.md` for each component and update
`evidence-matrix.md` as reports land.

Files dated `2026-06-22` for individual components are report scaffolds with
explicit commands and pass criteria. They remain pending until live output from
`guardian-mgmt` is pasted into the Result/Evidence fields.

Use `docs/runbooks/management-evidence.md` for the repo-owned commands that
collect live snapshots, rollout state, Talos health, and outage-rehearsal
state.

Use `src/infrastructure/evidence/` for opt-in Kubernetes Jobs and backup/restore
objects that generate load-test and disaster-recovery evidence. These resources
are not part of the steady-state Flux base.

Use `aspect infra evidence-verify-suite` before marking the evidence matrix
complete. The suite report ties one passing load/DR capture to one passing
all-node hardware outage capture and verifies all expected management nodes from
the checked-in inventory.

Use `aspect infra management-evidence-run` for the final live evidence package.
It writes a single parent directory containing the verified load/DR capture,
the verified all-node hardware outage capture, and the suite report.

After the component reports and `evidence-matrix.md` have been filled from that
final live-run package, run:

```sh
aspect infra reports-verify \
  --live-run-dir docs/reports/infrastructure/live-runs/<timestamp>-management-evidence
```

This writes `report-verification.tsv` into the live-run directory and fails if a
component report is missing required sections, still contains pending result
markers, lacks section-local passing results and evidence links for preflight,
load, DR, and outage evidence, does not reference the final live run, or if the
evidence matrix claims completion without a passing suite.

Each component report should cover:

- load test scope, command, inputs, and result;
- disaster-recovery drill procedure and result;
- single-node outage exercise procedure and result;
- observed recovery signals from Kubernetes, Cozystack, storage, and ingress;
- unresolved risk or follow-up work.

Do not include credentials, tokens, kubeconfigs, OpenBao recovery material, or
raw provider state in reports.
