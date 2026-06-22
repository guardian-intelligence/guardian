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

Each component report should cover:

- load test scope, command, inputs, and result;
- disaster-recovery drill procedure and result;
- single-node outage exercise procedure and result;
- observed recovery signals from Kubernetes, Cozystack, storage, and ingress;
- unresolved risk or follow-up work.

Do not include credentials, tokens, kubeconfigs, OpenBao recovery material, or
raw provider state in reports.
