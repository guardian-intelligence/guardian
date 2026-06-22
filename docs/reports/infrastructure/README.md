# Infrastructure Verification Reports

This directory holds checked-in evidence for new management-cluster
infrastructure components.

Use `templates/component-operational-report.md` for each component and update
`evidence-matrix.md` as reports land.

Each component report should cover:

- load test scope, command, inputs, and result;
- disaster-recovery drill procedure and result;
- single-node outage exercise procedure and result;
- observed recovery signals from Kubernetes, Cozystack, storage, and ingress;
- unresolved risk or follow-up work.

Do not include credentials, tokens, kubeconfigs, OpenBao recovery material, or
raw provider state in reports.
