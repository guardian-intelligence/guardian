# Management evidence plan status

Date: 2026-06-22

## Scope

This report records the repo-owned evidence command surface added before live
cluster convergence. It does not claim that load, disaster-recovery, or
single-node outage drills have passed.

## Added Command Surface

Preflight:

```sh
aspect infra preflight
```

Live convergence and readiness:

```sh
aspect infra apply-base --kubeconfig "${KUBECONFIG}"
aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"
aspect infra live-rollout --kubeconfig "${KUBECONFIG}"
aspect infra talos-health --talosconfig "${TALOSCONFIG}"
```

Kubernetes-side outage rehearsal:

```sh
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-cordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-drain --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-uncordon --kubeconfig "${KUBECONFIG}" --node <node>
```

## Current Evidence

- OpenTofu roots can be validated without remote state access.
- The company-site OCI image builds locally by digest.
- The Cozystack base renders through the repo-pinned kubectl.
- Live Kubernetes evidence is pending because the `guardian-mgmt` kubeconfig and
  converged cluster are not present in this workspace.
- Latitude adoption is pending a Latitude token and VLAN assignment import IDs.
- Cloudflare DNS state has been partially adopted but DNS changes have not been
  applied.

## Remaining Evidence Required

Load reports:

- Talos / Kubernetes API VIP.
- LINSTOR / DRBD storage.
- OpenBao.
- CNPG / Postgres.
- Harbor.
- ClickHouse.
- Cozystack Dashboard.
- Public ingress / DNS.
- Company site dev/gamma/prod.

Disaster-recovery reports:

- backup and restore-to-copy for CNPG / Postgres.
- backup and restore-to-copy for ClickHouse.
- Harbor push/pull after storage recovery.
- OpenBao raft/PVC recovery under single-node loss.
- LINSTOR / DRBD volume survival.

Single-node outage reports:

- one run per management node, including before/down/after snapshots.
- true hardware outage through Latitude OOB/API power control, not only
  `kubectl drain`.

## Risk Register

- Postgres and ClickHouse DR cannot be considered ready while their managed
  backup classes, S3 credentials, backup plans, and restore drills are absent.
- A Kubernetes drain rehearsal is useful but insufficient for the final
  single-node outage criterion.
- The company-site deployment references Harbor by digest; it cannot pull until
  Harbor is live and the image has been published there.
