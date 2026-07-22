# Architecture Decision Records

An ADR records one decision: the forces at play, what we chose, and what it costs.
ADRs are **immutable** — when a decision changes, write a new ADR that supersedes the
old one and flip the old one's status to Superseded. Never edit an Accepted ADR to
track reality; ADRs are history, not state. Current truth lives in code and manifests.

**When an ADR and the code disagree, the code is right.** Dismiss the ADR, then fix it
or supersede it. Every ADR ends with a `Related source` line naming the files that
embody its decision — that pointer, not the prose, is the document's tether to
reality: an ADR whose related source no longer exists has expired.

`docs/` has three lanes:

- **`docs/adrs/`** — why a past fork was taken (this directory).
- **Living policy docs** (`docs/*.md`) — present-tense SLOs and operating manuals,
  kept thin enough that one human can hold them in their head.
- **Runbooks** (`src/infrastructure/runbooks/`) — executable procedures.

## Index

| ADR | Title | Status | Date |
| --- | --- | --- | --- |
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted | 2026-07-11 |
| [0002](0002-analytics-event-storage-and-wire-contract.md) | Analytics event storage and wire contract | Accepted | 2026-07-04 |
| [0003](0003-validate-rendered-manifests.md) | Validate rendered manifests, not source templates | Accepted | 2026-07-02 |
| [0004](0004-stages-are-cozystack-tenants.md) | Deployment stages are Cozystack tenants | Accepted | 2026-06-27 |
| [0005](0005-no-in-cluster-object-storage.md) | No in-cluster object storage; R2 is the object tier | Accepted | 2026-07-07 |
| [0006](0006-dark-bundle-tooling.md) | Hauler builds and serves the dark bundle | Accepted | 2026-07-05 |
| [0007](0007-openbao-policy-management.md) | OpenBao policy changes ride PRs, not a policy controller | Accepted | 2026-07-04 |
| [0008](0008-dark-bundle-as-distribution.md) | The dark bundle is product distribution, not just DR | Accepted | 2026-07-03 |
| [0009](0009-node-local-zfs-not-ceph.md) | VM substrate storage is node-local ZFS, not Ceph | Accepted | 2026-07-11 |
| [0010](0010-two-release-signatures-one-format-per-lane.md) | Two release signatures, one format per lane | Accepted | 2026-07-12 |
| [0011](0011-three-replica-tigerbeetle.md) | TigerBeetle uses three production replicas | Accepted | 2026-07-17 |
| [0012](0012-tigerbeetle-owns-credit-reservations.md) | TigerBeetle owns customer-credit reservations | Accepted | 2026-07-17 |
| [0013](0013-bind-jobs-after-local-runner-assignment.md) | Bind jobs after local runner assignment | Accepted | 2026-07-21 |
