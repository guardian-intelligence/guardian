# Postflight scheduling and control plane

Status: end-state architecture, 2026-07-24. The control plane is one
deployable binary over Postgres and OpenBao. It owns truth, admission, plans,
and evidence — and deliberately does not own workflow orchestration or
runner selection, because GitHub already does.

## Substrate rules

- Postgres is the only durable state. Inbox/outbox rows, short transactions,
  idempotency keys, `FOR UPDATE SKIP LOCKED` workers. No workflow engine:
  GitHub owns the job DAG and retries; an internal engine would duplicate an
  authority we cannot override.
- Four independent ledgers, each advanced only by the controller that owns
  it: **capacity** (hosts, slots, pool-member incarnations), **demand and
  assignment** (job intents, plans, immutable bindings), **storage** (scopes,
  generations, manifests, pointers), **usage** (billable and reserved
  intervals). There is no state machine that joins them.
- Classes, hardware, and policy are rows, not enums. Adding a SKU, a
  hardware class, or a trust rule is data.

## The truth model

Three layers, strongest last:

1. **Webhooks are hints.** Delivery and order are unreliable — a `queued`
   event can arrive minutes after the job was assigned, or never. Ingress
   verifies the HMAC before parsing, records a durable delivery ledger, and
   treats every event as a trigger, not a fact.
2. **The REST API is truth.** Any event, gap, or deadline can trigger an
   authoritative refresh that constructs missing intent or corrects state.
3. **The guest's observed assignment is final.** The binding reported from
   inside the selected guest at the `acquirejob` commit point outranks
   anything GitHub's eventing implies (ADR 0013). On Confidential that
   observation arrives over the attested session, so it is also
   host-tamper-proof.

## Modules

| Module | Responsibility |
| --- | --- |
| GitHub ingress | Webhook inbox, signature verification, delivery ledger, API truth repair, cancellation and completion tracking |
| Admission | Label → runner class → fleet + hardware class routing; tenant concurrency; trust class; spend policy against the always-on meter |
| Capacity | Hosts, slots, pool-member incarnations, cordon/drain/offline; per-site inventories |
| Planning | Select the scope and a compatible generation for a job intent — never a VM; push small plans to every eligible host |
| Assignment | Persist the exact, immutable binding after the guest reports it; unique-match join or recycle |
| Generation catalog | Signed manifests, lineage, generation numbers, current-pointer CAS, rollback floors, retention |
| Attested sessions | Verify SNP evidence, establish sealed channels, release JIT and tenant keys (Confidential); authenticated control channel and DEK release (Lightning) |
| Transit custody | The `transit-postflight` client: tenant keys, manifest signing, crypto-erase |
| Metering | Append-only intervals: customer-billable and infrastructure-reserved |
| Reconcilers | Missed webhooks, stale hosts, deadlines, promotion, retention sweeps, image freshness |
| Canary controller | Dispatch real workflows as an ordinary showback tenant |

## Admission and routing

The runner label is the product SKU is the runner class. Admission resolves
it to a fleet and hardware class, checks tenant concurrency and trust class,
and applies spend policy. The millisecond meter is always on; deal shapes
(caps, commitments) are policy on top of the same meter. Webhook ingress
never rejects on backpressure — the queue is the buffer, time-to-pickup is
the SLO, and refusal happens only at the staleness deadline.

Jobs requiring `/dev/kvm` or other non-TEE-only capabilities are admissible
only to Lightning classes; the capability set lives on the class row.

## Plans and assignment

Planning happens before GitHub chooses anything:

1. A job intent exists (webhook or repair).
2. Planning selects the **scope** (see [storage](postflight-storage.md)) and
   the current compatible generation, and pushes the plan to every host of
   the class with a listening slot.
3. GitHub's broker hands the job to one connected listener; that guest
   reports the binding before Runner.Worker is created; the control plane
   joins it to the queued intent requiring a **unique** match — ambiguity
   recycles the member rather than guessing.
4. The assignment row is append-oriented: identity never rewrites, only
   state, timing, and results advance.

There is no placement steering: the control plane cannot route a job to the
host holding its warm generation, and does not try. Locality is emergent —
a scope's generations live where its jobs ran — and an off-home win runs
cold and re-establishes residency by sealing there. After `acquirejob` there
is no provider give-back: losing the guest fails that attempt closed, and
the ledger never claims a transparent requeue.

## Pool supply

Slots are refilled ahead of demand: launch the generic guest, verify
attestation, establish the sealed session, mint the JIT configuration, start
the listener — all before any customer job exists, so provisioning is never
on the customer path. JIT configurations are single-use, exist only in guest
RAM, and are minted per member, per registration.

## Reconcilers

Idempotent, independently scheduled, each owning one repair:

- API truth sweeps for gaps in webhook delivery.
- Host liveness: a silent host's members go stale; its slots leave capacity.
- Deadlines: every non-terminal state has one; expiry is a first-class
  transition, not an alert.
- Promotion: attempt-specific success promotes candidates by CAS; failures
  discard; ambiguity retains the previous pointer.
- Retention: reap is a control-plane verb executed by hostd, driven by
  last-use, size, and pins.
- Image and runner freshness: GitHub's runner deprecation clock and our
  golden-image cadence both feed the same reconciler; a stale image drains
  its members.

## Canary and showback

The canary is an ordinary customer in every mechanical respect: a real
GitHub App installation, tenant, repository, and runner labels, dispatched on
a schedule by the canary controller. Its workflows are customer workloads,
never cluster administration. Every run traverses admission, planning,
assignment, attestation, keys, storage, and metering, and accrues real
showback usage that never settles.

Standing scenarios: cold job; exact warm restore; injected recoverable CRIU
incompatibility; integrity failure that must recycle; cancellation before
and after acquisition; hostd restart with adoption; rollback-floor refusal;
checkpoint timeout; runner and image version expiry. Each scenario asserts
its span set (below), so a regression pages before a customer feels it.

## Metering and moments

Three moments partition every job's timeline:

| Moment | Meaning | Meter |
| --- | --- | --- |
| `customer_complete` | The attempt concluded on GitHub | Customer-billable time ends here |
| `slot_reusable` | Donor destroyed, slot released for refill | Reserved-infrastructure time |
| `generation_published` | Candidate promoted after attempt success | Reserved-infrastructure time |

Sealing and publication always run on Guardian's clock, never the
customer's. Intervals are append-only; corrections are compensating rows.

Required hot-path spans (source-local monotonic clocks, realtime brackets
for cross-machine joins; never subtract monotonics across boot IDs):

```text
queued → assigned
assigned → plan consumed
clone → attach → mounts ready
mounts → restore result (restored | cold | unsafe)
restore ready → Worker authorized
customer complete → slot reusable
slot reusable → replacement listening
candidate → promoted
```

Metric labels stay low-cardinality (class, fleet, outcome); repository,
tenant, generation, and assignment identities belong in traces, logs, and
ClickHouse analytics.

## State machines

Owned per ledger; states are validated text, deadlines are columns.

```text
slot        empty → booting → attesting → preparing → listening
            → assigned → rendezvous → running → checkpointing → destroying → empty

assignment  observed → plan-consumed → materializing → mounting → restoring
            → authorizing → running → customer-complete → sealing → terminal

generation  candidate → authenticated → current
            → retained → reaped        ↘ discarded / quarantined
```

Related: [architecture](postflight-architecture.md) ·
[storage](postflight-storage.md) ·
[runner lifecycle](postflight-runner-lifecycle.md) ·
[ADR 0013](adrs/0013-bind-jobs-after-local-runner-assignment.md)
