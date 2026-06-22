# Live Infrastructure Evidence Runs

`aspect infra evidence-capture` writes timestamped capture directories here by
default. Each capture contains a `MANIFEST.md`, `summary.tsv`, Kubernetes
snapshots, evidence Job logs, database backup/restore state, and Talos health
when a talosconfig is supplied.

Commit only live captures that support an infrastructure report. Do not commit
operator kubeconfigs, talosconfigs, OpenBao tokens, Cloudflare credentials, or
raw Kubernetes Secret values.
