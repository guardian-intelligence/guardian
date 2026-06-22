# Talos / Kubernetes API VIP Operational Report

## Scope

- Component: Talos control plane, Kubernetes API, Layer2 VIP.
- Desired state source: `src/infrastructure/talm/`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: management control plane.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight`.
- Reconciled resources: three Talos control-plane nodes on VLAN `2140`, API VIP
  `10.8.0.250`.
- Healthy baseline command: `aspect infra talos-health --talosconfig "${TALOSCONFIG}"`.
- Result: pending; no live `guardian-mgmt` Talos config is present here.

## Load Test

- Command: repeat `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"` and
  `aspect infra talos-health --talosconfig "${TALOSCONFIG}"` while evidence
  overlay jobs run.
- Inputs: endpoints `10.8.0.250`, nodes `10.8.0.11,10.8.0.12,10.8.0.13`.
- Pass criteria: every API read succeeds, Talos health passes, etcd reports
  three voting members, and no kube-apiserver VIP failover is visible.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: wipe and rebuild one control-plane node from repo-declared
  Talos/Talm state.
- Restore source: checked-in Talm values plus regenerated local secret-zero
  material.
- Pass criteria: rebuilt node rejoins etcd and Kubernetes without hand-editing
  cluster objects; API VIP remains reachable through quorum.
- Result: pending.

## Single-Node Outage Exercise

- Node removed or powered off: each management node, one at a time.
- Procedure: Latitude OOB/API power-off, then `aspect infra talos-health` and
  `aspect infra live-snapshot`, then power-on and repeat.
- Expected behavior: API VIP remains reachable and etcd remains quorate.
- Result: pending.

## Residual Risk

- Hardware outage drill depends on Latitude API credentials and live cluster
  access.
