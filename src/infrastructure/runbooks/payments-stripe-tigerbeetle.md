# Stripe sandbox payment rail

Guardian's payment control plane accepts Stripe sandbox facts, durably records
them in PostgreSQL, journals every ledger mutation to R2, and projects accepted
movements into TigerBeetle ledger `2`. Customer checkout remains disabled;
ledger `1` admission requires the lifecycle and reconciliation gates in
`docs/tigerbeetle-financial-model.md`.

## Ownership

- Stripe owns payment-method handling, hosted Checkout, charges, fees, and
  signed provider events. It is not the balance authority.
- The payments service owns webhook verification, durable event ingestion,
  provider re-fetch, idempotency, and orchestration.
- PostgreSQL owns business IDs, provider payloads, processing state, and
  reconciliation cursors.
- R2 owns immutable intent/result/outcome evidence around every TigerBeetle
  mutation.
- TigerBeetle owns accepted synthetic balances and movements.
- ClickHouse owns trace evidence and queryable operational history, not
  financial authority.

The service stores stable TigerBeetle IDs before submission. A retry sends the
same ID and payload. A timeout after submission is therefore resolved by
retrying, never by inventing a compensating balance.

## Stripe credentials and resources

Use two restricted sandbox keys:

1. `stripe_e2e_provisioner_sandbox_key` is used only by the
   `guardian-stripe-sandbox` OpenTofu root. Grant Account Read, Products Write,
   Prices Write, and Webhook Endpoints Write.
2. `stripe_e2e_bootstrap_sandbox_key` becomes the runtime key. Grant Account
   Read; Payment Intents Read and Write; Checkout Sessions Read and Write;
   Charges Read; Balance Transactions Read; and Products and Prices Read.

Neither key is a live-mode key. Do not reuse the provisioner key in the
workload. The runtime key must not have Webhook Endpoints Write.

The official `stripe/stripe` provider manages the synthetic product, USD price,
and the single webhook endpoint. The endpoint subscribes only to
`payment_intent.succeeded` and pins the same API version as the runtime client.
The R2-backed OpenTofu state is credential-bearing because Stripe returns a
webhook signing secret only when the endpoint is created.

The current provider root is:

```text
src/infrastructure/bootstrap/guardian-stripe-sandbox
```

Its managed product and price carry `guardian_synthetic=true`,
`guardian_ledger_id=2`, and `guardian_environment=sandbox`. These fields are
the auditor-visible boundary between continuous probes and customer records.

## Release and runtime topology

The payments service runs two replicas in `tenant-guardian-prod` behind a
Flagger blue/green service. A release must pass:

1. readiness against PostgreSQL and all three TigerBeetle addresses;
2. a real Stripe sandbox PaymentIntent through webhook projection and
   TigerBeetle ledger `2`; and
3. readiness under rollout load.

Kargo tracks the signed `edge` digests for `payments` and `payments-canary`,
updates both workload pins and release-manifest entries in one PR, and
auto-promotes only after repository checks pass. Flagger keeps public traffic
on the primary color unless all rollout gates succeed.

The runtime network policy permits only:

- root-ingress traffic to the public payments paths;
- the shared encrypted CNPG `postgres-products` service;
- TigerBeetle at `10.8.0.11:3000`, `10.8.0.12:3000`, and
  `10.8.0.13:3000`;
- Stripe and R2 over public TLS;
- OTLP to the in-cluster collector;
- Flagger and scheduled canary traffic; and
- VictoriaMetrics scraping.

TigerBeetle is never exposed by a Kubernetes or public Service.

## Continuous checkout proof

Two five-minute canaries are intentionally synthetic:

- The rail canary creates and confirms a sandbox PaymentIntent and requires a
  successful TigerBeetle ledger `2` projection.
- The browser canary opens Guardian in Chromium, propagates W3C
  `traceparent`, creates hosted Stripe Checkout, pays with a Stripe test card,
  waits for the signed webhook projection, and verifies the full trace in
  ClickHouse.

The browser run passes only when one trace contains all of:

```text
POST /api/payments/v1/canary/checkout-session
checkout.create_session
stripe.payment_intent.succeeded
tigerbeetle.project_payment
```

The ClickHouse credential is a Cozystack-managed read-only user and is mounted
only in the short-lived browser Job.

## Operational tracks

Treat readiness as six independent tracks:

1. Rail correctness: signed Stripe facts, authoritative re-fetch, exact
   amount/currency/fee arithmetic, and deterministic TigerBeetle movements.
2. Security: sandbox/account binding, restricted keys, OIDC organization
   identity, encrypted storage, immutable journal, and network isolation.
3. Release safety: signed images, Kargo promotion, Flagger gates, rollback,
   and public-edge health.
4. Resilience: three-node TigerBeetle quorum, encrypted CNPG, R2 evidence,
   backup/restore, and complete node-failure drills.
5. Observability: metrics, logs, browser-to-ClickHouse traces, reconciliation
   evidence, and durable canary run IDs.
6. Continuous verification: positive rail and checkout canaries plus induced
   negative tests proving both silence below thresholds and alarms above them.

## Alert contract

The payment alerts cover:

- no scrapeable service replica;
- loss of Stripe sandbox-account binding;
- old provider-event backlog;
- incomplete TigerBeetle recovery journal;
- unmatched Stripe balance transactions;
- stale balance reconciliation;
- stale or failed rail and browser canaries;
- more than five invalid webhook signatures in five minutes;
- any live-mode event reaching the sandbox endpoint; and
- failed scheduled canary Jobs.

One invalid signature is expected to increment the rejection metric without
alerting. A burst above the threshold must alert. A live-mode event must be
rejected before persistence or ledger mutation and must alert.

## Evidence queries

Latest durable canary state:

```sh
curl -fsS -H "Authorization: Bearer $PAYMENTS_CANARY_TOKEN" \
  http://payments.tenant-guardian-prod/internal/payments/canary/runs/latest
```

Service metrics:

```sh
kubectl port-forward -n tenant-guardian-prod svc/payments-metrics 18080:8080
curl -fsS http://127.0.0.1:18080/metrics
```

The authoritative ClickHouse table for end-to-end spans is
`guardian_analytics.otel_traces`.

## Secret disposal

Credential staging files are temporary. Delete
`/home/ubuntu/.claude/DELETE_ME.env` only after the Stripe root has applied,
the scoped OpenBao values are synchronized, both canaries pass, induced alert
tests finish, and no rerun requires the original keys.
