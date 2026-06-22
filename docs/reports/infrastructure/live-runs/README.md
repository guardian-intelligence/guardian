# Live Infrastructure Evidence Runs

`aspect infra evidence-capture` writes timestamped capture directories here by
default. Each capture contains a `MANIFEST.md`, `summary.tsv`, Kubernetes
snapshots, evidence Job logs, database backup/restore state, and Talos health
when a talosconfig is supplied.

Run `aspect infra evidence-verify --run-dir <capture>` before committing a
capture. The verifier writes `VERIFY.md` and `verification.tsv` into the
capture directory so component reports can cite both raw evidence and
machine-checked pass/fail results.

Commit only live captures that support an infrastructure report. Do not commit
operator kubeconfigs, talosconfigs, OpenBao tokens, Cloudflare credentials, R2
credentials, or raw Kubernetes Secret values.
