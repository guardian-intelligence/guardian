# Public Ingress / DNS Operational Report

## Scope

- Component: public ingress and Cloudflare DNS.
- Desired state source: `src/infrastructure/bootstrap/cloudflare-dns/`,
  `src/infrastructure/base/cozystack/platform.yaml`,
  `src/infrastructure/evidence/http-load.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: public management ingress.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra dns-plan`, `aspect infra preflight`.
- Reconciled resources: Cloudflare A records for apex, dev, gamma, oci,
  dashboard, and grafana.
- DNS target IPs are derived from `nodes[*].public_ipv4` in
  `src/infrastructure/inventory/guardian-mgmt.json`; there is no second
  checked-in public-IP list for the Cloudflare root.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: read-only `aspect infra dns-plan` succeeded against the remote R2
  OpenTofu backend and Cloudflare API. Current plan is 14 A records to add and
  3 existing records to update; no records are planned for deletion. Not
  applied. Apex and `oci.guardianintelligence.org` still plan to move away from
  `206.223.228.99`, so apply is held until the management cluster is ready for
  public traffic.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, and
  `aspect infra evidence-logs`.
- Inputs: all managed public hostnames from
  `src/infrastructure/inventory/guardian-mgmt.json`.
- Evidence source: `Job/tenant-root/evidence-http-load` logs one
  `http-target` line per public route with `remote_ips=` from curl's connected
  remote IP; `aspect infra evidence-verify` checks every observed IP against
  `nodes[*].public_ipv4` in `src/infrastructure/inventory/guardian-mgmt.json`.
- Pass criteria: observed DNS/connection targets are only declared management
  node public IPs, TLS is valid, and HTTP evidence requests report zero
  failures.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: remove one ingress node from service by powering off a
  management node.
- Restore source: remaining public node IPs and Cozystack ingress controllers.
- Pass criteria: HTTP evidence succeeds during the node outage and every
  observed `remote_ips=` value remains in the declared management public IP
  set.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run HTTP evidence before, during, and after each one-node outage.
- Expected behavior: public routes remain reachable and no route resolves or
  connects to an undeclared public IP.
- Result: pending.

## Residual Risk

- Cloudflare DNS has been partially adopted into state but not applied from
  this workspace. The planned apex and `oci.guardianintelligence.org` updates
  are intentional traffic moves and must remain gated on live cluster readiness.
