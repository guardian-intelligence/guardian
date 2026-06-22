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
- Result: pending.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, and
  `aspect infra evidence-logs`.
- Inputs: all managed public hostnames from
  `src/infrastructure/inventory/guardian-mgmt.json`.
- Pass criteria: DNS resolves only to the declared management node public IPs,
  TLS is valid, and HTTP evidence requests report zero failures.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: remove one ingress node from service by powering off a
  management node.
- Restore source: remaining public node IPs and Cozystack ingress controllers.
- Pass criteria: DNS still returns healthy node IPs and HTTP evidence succeeds.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run HTTP evidence before, during, and after each one-node outage.
- Expected behavior: public routes remain reachable.
- Result: pending.

## Residual Risk

- Cloudflare DNS has been adopted into state but not applied from this
  workspace.
