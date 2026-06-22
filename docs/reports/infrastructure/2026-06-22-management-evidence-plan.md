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
aspect infra inventory-check
```

Live convergence and readiness:

```sh
aspect infra apply-base --kubeconfig "${KUBECONFIG}"
aspect infra seed-db-backup-secret --kubeconfig "${KUBECONFIG}"
aspect infra publish-company-site
aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"
aspect infra live-rollout --kubeconfig "${KUBECONFIG}"
aspect infra talos-health --talosconfig "${TALOSCONFIG}"
```

Opt-in load and DR evidence fixtures:

```sh
aspect infra evidence-render
aspect infra evidence-clean --kubeconfig "${KUBECONFIG}"
aspect infra evidence-apply --kubeconfig "${KUBECONFIG}"
aspect infra evidence-wait --kubeconfig "${KUBECONFIG}" --timeout 30m
aspect infra evidence-restore-apply --kubeconfig "${KUBECONFIG}"
aspect infra evidence-restore-wait --kubeconfig "${KUBECONFIG}" --timeout 30m
aspect infra evidence-logs --kubeconfig "${KUBECONFIG}"
aspect infra evidence-snapshot --kubeconfig "${KUBECONFIG}"
aspect infra evidence-capture --kubeconfig "${KUBECONFIG}" --phase evidence
aspect infra evidence-run --kubeconfig "${KUBECONFIG}" --phase evidence --timeout 30m
aspect infra evidence-verify --run-dir docs/reports/infrastructure/live-runs/<timestamp>-evidence --mode evidence --require-talos
```

Kubernetes-side outage rehearsal:

```sh
aspect infra outage-snapshot --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-cordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-drain --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-uncordon --kubeconfig "${KUBECONFIG}" --node <node>
aspect infra outage-run --kubeconfig "${KUBECONFIG}" --node <node> --timeout 10m
aspect infra evidence-verify --run-dir docs/reports/infrastructure/live-runs/<timestamp>-outage-after --mode outage --node <node>
LATITUDESH_AUTH_TOKEN="${LATITUDESH_AUTH_TOKEN}" aspect infra hardware-outage-run --kubeconfig "${KUBECONFIG}" --talosconfig "${TALOSCONFIG}" --node <node> --require-talos
```

## Current Evidence

- OpenTofu roots can be validated without remote state access.
- Provider-free inventory checks now compare
  `src/infrastructure/inventory/guardian-mgmt.json` against Talm, Cozystack,
  MetalLB, kube-ovn MTU, environment, tenant, DNS, and company-site manifests
  without live state; they also assert the Flux base contains the required
  Harbor, ClickHouse, Postgres/CNPG, OpenBao, Cozystack platform, storage,
  networking, tenant, and company-site manifests.
- The company-site OCI image builds locally by digest.
- The Cozystack base renders through the repo-pinned kubectl.
- Postgres and ClickHouse now have declared R2 backup plumbing,
  `BackupClass` objects, and hourly `Plan` objects.
- Opt-in Kubernetes evidence fixtures now exist for HTTP load, Harbor digest
  reads, OpenBao write-read load, replicated PVC smoke,
  Postgres/ClickHouse write-read load, and Postgres/ClickHouse
  backup/restore-to-copy.
- Harbor publication now has a repo-owned `rules_oci` push target and Aspect
  task for the company-site image.
- R2 backup credential delivery now has a repo-owned Aspect task that applies
  the Kubernetes Secret from environment variables through stdin.
- OpenBao evidence token delivery now has a repo-owned Aspect task that applies
  the Kubernetes Secret from environment variables through stdin.
- Live evidence capture now has a repo-owned read-only Aspect task that writes
  Kubernetes, evidence Job, database restore, and Talos outputs under
  `docs/reports/infrastructure/live-runs/` for check-in with component reports.
- Captured evidence now has a repo-owned `aspect infra evidence-verify` task
  that writes `VERIFY.md` and `verification.tsv` into the live-run directory
  before it is attached to component reports.
- The full opt-in load/DR sequence now has a repo-owned `aspect infra
  evidence-run` task that runs clean/apply/wait/restore/logs/snapshot/capture
  and captures degraded state when a step fails.
- The Kubernetes-side single-node outage rehearsal now has a repo-owned
  `aspect infra outage-run` task that captures before/drained/after evidence
  and preserves failed-state output.
- The hardware outage runbook now records the exact Latitude power-action HTTP
  contract and identifies the checked-in inventory as the source for server IDs.
- True hardware single-node outage rehearsal now has a repo-owned
  `aspect infra hardware-outage-run` task. It builds the pinned Latitude power
  helper, powers one management node off/on through the Latitude API, captures
  before/down/after evidence, and runs the evidence verifier for each phase.
- Live Kubernetes evidence is pending because the `guardian-mgmt` kubeconfig and
  converged cluster are not present in this workspace.
- Latitude adoption is pending a Latitude token and VLAN assignment import IDs.
- Cloudflare DNS state has been partially adopted but DNS changes have not been
  applied.
- Cloudflare A-record targets are derived from the management node inventory
  (`nodes[*].public_ipv4`) rather than a separate DNS IP list.
- A read-only `aspect infra dns-plan` against remote state succeeded: 14 A
  records to add, 3 records to update, and 0 to destroy. Apply remains gated
  because apex and `oci.guardianintelligence.org` still move public traffic from
  `206.223.228.99` to the management cluster IP set.
- R2 backup credential values remain a secret-zero bootstrap input and must be
  present in the operator environment before `aspect infra seed-db-backup-secret`
  is run.
- The OpenBao evidence token remains a secret-zero bootstrap input and must be
  present in the operator environment before
  `aspect infra seed-openbao-evidence-token` is run.

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

- live backup and restore-to-copy for CNPG / Postgres.
- live backup and restore-to-copy for ClickHouse.
- Harbor push/pull after storage recovery.
- OpenBao raft/PVC recovery under single-node loss.
- LINSTOR / DRBD volume survival.

Single-node outage reports:

- one run per management node, including before/down/after snapshots.
- true hardware outage through Latitude OOB/API power control, not only
  `kubectl drain`.

## Risk Register

- Postgres and ClickHouse DR cannot be considered ready until the R2 Secret
  seeding task has been run and live backup/restore-to-copy drills pass.
- A Kubernetes drain rehearsal is useful but insufficient for the final
  single-node outage criterion.
- Hardware outage evidence is still pending live execution on each management
  node with a Latitude token, kubeconfig, and talosconfig.
- The company-site deployment references Harbor by digest; it cannot pull until
  Harbor is live, OCI auth is present, and `aspect infra publish-company-site`
  has pushed the image there.
