# Live Infrastructure Evidence Runs

`aspect infra evidence-capture` writes timestamped capture directories here by
default. Each capture contains a `MANIFEST.md`, `summary.tsv`, Kubernetes
snapshots, evidence Job logs, database backup/restore state, and Talos health
when a talosconfig is supplied.

`aspect infra management-evidence-run` writes a parent directory here with
`evidence/`, `hardware-outage-all/`, and `management-suite/` children. That is
the preferred final live evidence package for the management cluster.

Run `aspect infra evidence-verify --run-dir <capture>` before committing a
capture. The verifier writes `VERIFY.md` and `verification.tsv` into the
capture directory so component reports can cite both raw evidence and
machine-checked pass/fail results.

`aspect infra hardware-outage-run-all` writes a parent directory here containing
one per-node hardware outage directory. Each per-node directory has Latitude
JSONL status/action records plus `outage-before`, `outage-down`, and
`outage-after` capture subdirectories.

`aspect infra evidence-verify-suite` reads one verified load/DR evidence
capture and one all-node hardware outage parent directory, then writes a
suite-level `SUITE.md` and `suite-verification.tsv` into a separate output
directory. Commit that suite directory with the raw captures it references.

Commit only live captures that support an infrastructure report. Do not commit
operator kubeconfigs, talosconfigs, OpenBao tokens, Cloudflare credentials, R2
credentials, or raw Kubernetes Secret values.
