# 0001 — Record architecture decisions

Status: Accepted · Date: 2026-07-11

## Context

Free-form design docs rot: written in the present tense, they read as claims about
the running system long after the system has moved on, and become a shorthand for
truth that nobody re-verifies. What actually needs durable recording is small — the
decisions, and the reasoning that would otherwise be re-litigated.

## Decision

We record architecture decisions as ADRs in `docs/adrs/`, one file per decision,
named `NNNN-kebab-title.md` with monotonically increasing numbers.

Each ADR carries a `Status: … · Date: …` header line, three sections — Context,
Decision, Consequences — and a closing `Related source` line naming the files that
embody the decision. ADRs are immutable once Accepted. A changed decision gets a *new* ADR stating
"Supersedes [NNNN](NNNN-kebab-title.md)"; the old ADR's status becomes
"Superseded by [MMMM](MMMM-kebab-title.md)". Statuses: Proposed, Accepted,
Superseded. The index in [README.md](README.md) is updated in the same commit.

Not ADRs: runbooks (procedures), SLO tables (living policy, present tense by
design), research notes (delete them once the decision they informed is recorded),
product documents.

## Consequences

- A dated, status-stamped record cannot silently become a false present-tense claim;
  the failure mode shifts from "doc lies about now" to "decision not yet recorded".
- Writing a new ADR to change course is deliberate friction: reversals leave a trail.
- The surface area stays small only if we resist migrating narrative docs wholesale;
  each ADR captures the decision core, and the winding material dies with the source.

Related source: [README.md](README.md)
