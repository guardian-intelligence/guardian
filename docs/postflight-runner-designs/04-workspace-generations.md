# 04 — Workspace generations (golden `$GITHUB_WORKSPACE` artifacts)

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

The zvol lifecycle that makes the second run of a job fast: every job runs
on a clone of its scope's current generation; a green run seals a new one;
promotion is a compare-and-swap gated on GitHub's truth. Cache is
acceleration, never semantic truth — a miss is an empty workspace, an
ambiguous seal is a skipped seal, and no job result ever changes because of
anything in this document.

## Scope key (which jobs share a lineage)

```
(org, repo, scope_ref, workflow_path, job_name, matrix_key, runner_class, platform_image_id)
```

Job-shape dimensions are included deliberately: without them, `lint`,
`test`, and `build` alternate ownership of one lineage, polluting each
other's artifacts and losing every CAS race. `scope_ref` is the default
branch for push/main jobs and the **target** branch for PRs: PR jobs read
the target's current generation and their writes are never promoted. All
dims come from the `workflow_job` webhook payload. Cost of the extra dims is
more lineages on NVMe — exactly what the hammer measures (06).

## States

Generation: `candidate → committed → retained → reapable → reaped`, plus
`discarded` (failed/skipped seals). Pointer: one
`current_generation_id` per scope, advanced only by CAS. The per-job
operation journal on the host: `requested → mounted → sealed →
committed | skipped | failed`.

Invariants (each gets a `sim/`-style check with a vacuity mutant, extending
the existing `hostd/sim` model):

1. The pointer never references a generation that isn't `committed`+.
2. Reap never destroys a dataset referenced by the pointer, a running
   operation, or a pin.
3. PR-scope writes never reach `committed`.
4. Ambiguity never advances anything (quiesce failure, ZFS error, unknown
   GitHub conclusion ⇒ `skipped`; previous current stays authoritative).
5. Journal recovery is total: every crash point between journal rows maps to
   exactly one classified recovery action.

## Lifecycle mechanics

**Clone (after local assignment).** The immutable assignment records the
scope's `source_generation` (this exact value is the CAS guard later).
hostd clones that generation's snapshot into the assignment's workspace,
tool, and process zvols; no generation creates an empty capsule (cold path;
the customer's first green run seeds the lineage).

**Seal (at runner exit 0).** guestd quiesces (01) → hostd
`zfs snapshot ws/<assignment>@sealed`, records `used/logicalused/written/
compressratio` (06 consumes these) → reports `sealed{snapshot_guid}` in the
next sync. The VM is destroyed and the slot released immediately —
occupancy never waits on GitHub. Exit ≠ 0: no snapshot, dataset destroyed,
operation `skipped`.

**Commit + promote (controlplane).** The deadline/truth reconciler already
observes attempt-specific job conclusions (`github.job.terminal.observed`).
On `success` for an assignment with a sealed candidate: mark `committed`, then

```sql
UPDATE workspace_scopes
SET current_generation_id = :new
WHERE scope_key = :scope
  AND current_generation_id IS NOT DISTINCT FROM :observed_source
```

Zero rows = lost the race ⇒ `retained`. Any other conclusion (failure,
cancelled, stale attempt, unknown) ⇒ `discarded`. Winner's dataset is
`zfs promote`d host-side on the next sync so reaping the parent stays legal.

**Retention.** Mechanism now, policy later: a sweep transitions
`retained/discarded → reapable` (never violating invariant 2), and reap
verbs already merged in `hostd/zvol` destroy `reapable` datasets on sync
dispatch. Policy inputs (`last_used_at`, `bytes`, `pinned`, pool watermarks
from host sync) are all live in the schema already.

**Host journal.** Every ZFS mutation on the assignment path writes an operation
row (id, verb, dataset, phase) *before* the effect — same
meta-before-effects discipline as the vm state-dir. PG locks are never held
across ZFS ops. Crash recovery classifies by (journal phase × dataset
existence) into: roll forward, roll back, or quarantine.

## Placement affinity (rendezvous)

A clone is ~free on the host where the source generation lives and a full
send away anywhere else. `workspace_scopes.home_host_id` records residency.
Pool placement can bias members toward that host before GitHub assigns a job;
after local assignment, hostd must rendezvous the generation with that exact
member. A remote miss becomes a cold capsule and remains correct.

## Schema

- `workspace_scopes` (new): scope key dims, `current_generation_id`,
  `home_host_id`.
- `workspace_generations` (exists): add `scope_id`, `state`,
  `parent_generation_id`, `snapshot_guid`, `sealed_at`, size stats columns.
- `runner_job_assignments`: immutable local job/member binding,
  `source_generation`, `workspace_scope_id`, restore outcome, and seal result.

## Pre-TEE scope

Plaintext zvols; generation identity = ZFS snapshot GUID. The SNP phase
keeps this lifecycle unchanged and adds guest-side LUKS2 keyed by the
PSP-derived, measurement-bound key (guestd's `snp` encryption mode), so the
host snapshots ciphertext. Claims and release gates live in the
[security model](../postflight-security-model.md); 01 specifies the
implementation seams.
