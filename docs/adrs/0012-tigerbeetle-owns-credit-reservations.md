# 0012 — TigerBeetle owns customer-credit reservations

Status: Accepted · Date: 2026-07-17

## Context

Guardian bills sandbox workload time at millisecond resolution. A customer may
fund usage through free or subscription allowance, scoped promotional credit,
account-wide credit, or purchased top-ups. Concurrent jobs must not spend the
same credit, and a reservation must be safely postable or voidable after the
work outcome is known.

TigerBeetle provides immutable two-phase transfers and pessimistically applies
account balance constraints when a transfer becomes pending. Keeping holds in
PostgreSQL would require the service layer to reproduce those concurrency and
balance guarantees while TigerBeetle reported no pending balance. That creates
two financial authorities and defeats the selected ledger's native primitive.

Product catalog concepts do not belong in the ledger. Products, buckets, SKUs,
plans, rate cards, and grant eligibility change independently from the
immutable numeric protocol used by TigerBeetle.

## Decision

Guardian uses one USD-denominated production credit ledger at asset scale 12
and a second, identically modeled synthetic ledger. Numeric ledger, account,
and transfer codes are fixed in
[`docs/tigerbeetle-financial-model.md`](../tigerbeetle-financial-model.md).

Every credit grant has one credit-positive TigerBeetle account with
`debits_must_not_exceed_credits`. PostgreSQL records the grant's source, SKU or
broader scope, validity, contract provenance, and mapping to that account.

Sandbox admission reserves the selected grant accounts with atomically linked
TigerBeetle pending transfers. Settlement posts all or part of those transfers;
failure voids them; a timeout is only a leak breaker. PostgreSQL persists the
IDs and policy context but does not maintain or subtract an independent held
amount.

Posted mistakes are corrected with new reversing and replacement transfers.
Provider events, fees, refunds, disputes, and payouts are represented on the
same currency ledger and reconciled to the provider's itemized balance
transactions and daily financial report. Stripe remains a payment rail and
does not own subscriptions, entitlements, usage, or customer documents.

## Consequences

- Concurrent admission is fail-closed and enforced at the financial system of
  record instead of by a service-local balance calculation.
- One usage window may create several linked pending transfers when scoped
  grants fund it. The gateway must retain the exact leg order and IDs.
- Long-running resources need bounded renewable reservations; a pending
  transfer cannot be an unbounded workload lease.
- SKU selection, upgrade policy, and custom pricing remain queryable and
  changeable in PostgreSQL without creating a ledger or account-code explosion.
- Grant expiry must wait for ordinary pending transfers to resolve before the
  account is swept and closed.
- Postpaid overage is not enabled until its upper limit is enforced with a
  reviewed TigerBeetle design. A payment method alone never authorizes debt.
- Asset scale, ledger IDs, and numeric codes are migration boundaries after
  first production use and may only evolve through new IDs and a superseding
  ADR.

Related source: `docs/tigerbeetle-financial-model.md`,
`docs/tigerbeetle.md`

