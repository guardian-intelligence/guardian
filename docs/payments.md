# Payments control and data plane

Guardian's payment plane is a sandbox-only production-shaped deployment.
Customer checkout is disabled and the service rejects live Stripe events.
Synthetic transactions use TigerBeetle ledger `2`; ledger `1` remains reserved
for admitted customer value after operational hardening.

## Identity and configuration

- Browser customer APIs validate tokens from the Guardian customer identity
  realm and persist only its subject. Product multitenancy and permissions are
  enforced through the typed Authorization API backed by SpiceDB.
- CNPG provides the three-replica `products` PostgreSQL cluster and its scoped
  database roles. CNPG is not a second customer identity provider.
- Stripe sandbox product, price, and webhook resources are managed by
  OpenTofu. Runtime Stripe, R2 journal, and canary credentials are projected
  from scoped OpenBao paths through External Secrets.
- The Stripe key is restricted to the sandbox resources and operations used by
  provisioning, payment creation, reconciliation, and webhook validation. The
  service rejects live-mode keys and events.

## Control plane

Flux owns every Kubernetes object. Kargo discovers signed payment-service and
canary images, updates digest pins through reviewed pull requests, and records
the promoted freight. Flagger controls service rollout and requires:

1. PostgreSQL, Stripe-account binding, and TigerBeetle readiness;
2. a real sandbox Stripe-to-TigerBeetle transaction; and
3. sustained readiness under load.

Customer checkout has a fail-closed feature gate. Enabling it requires a
reviewed source change after the ledger `1` operational-hardening gates pass.

## Data plane

The service is the only product caller admitted through the authenticated
TigerBeetle transport. A payment follows this path:

1. The browser calls the public payments API with a W3C trace context and a
   Keycloak identity. The synthetic endpoints instead require a dedicated
   constant-time-compared bearer token.
2. The service persists the order and correlation IDs in PostgreSQL before
   contacting Stripe.
3. Stripe sends signed webhooks to the public edge. The service verifies the
   signature, sandbox mode, and account binding before persisting the provider
   event.
4. The projector validates Stripe amount, currency, fee, net, metadata, and
   balance-transaction invariants, journals the exact TigerBeetle command to
   R2, and submits it through the three mutually authenticated replica
   connections.
5. PostgreSQL records the accepted result and immutable outcome. The
   reconciliation loop independently compares recent Stripe balance
   transactions, provider events, journal evidence, and ledger projection.
6. OpenTelemetry spans flow through the collector into ClickHouse. Financial
   analytics are evidence and read models; they are not the balance authority.

No personal data enters TigerBeetle. Synthetic orders use tenant
`synthetic-canary`, Stripe metadata `guardian_lane=synthetic`, and ledger `2`.

## Continuous verification

Two five-minute canaries exercise different entry paths:

- The rail canary calls the internal service endpoint and confirms a sandbox
  PaymentIntent server-side.
- The browser canary launches Chromium, loads the public canary page, and
  originates the same sandbox payment through the public edge. It passes only
  after the Stripe webhook is durable, TigerBeetle has accepted the projection,
  and ClickHouse contains all four spans:
  `POST /api/payments/v1/canary/checkout`,
  `canary.browser_to_tigerbeetle`,
  `stripe.payment_intent.succeeded`, and
  `tigerbeetle.project_payment`.

Stripe-hosted Checkout remains a manual acceptance surface. Continuous tests
do not automate Stripe's hosted form because its anti-card-testing controls are
designed to prevent browser automation. This follows [Stripe's automated
testing guidance](https://docs.stripe.com/automated-testing). Client-side
decline behavior is tested with simulated Stripe error objects; the real
canaries exercise server-side sandbox API responses and webhooks.

The payment rules alert on scrape loss, Stripe-account drift, provider backlog,
incomplete journal evidence, unmatched balance transactions, stale
reconciliation, stale or failed canaries, invalid-signature bursts, rejected
live-mode events, and failure of the latest scheduled canary Jobs. Historical
failed Jobs do not latch an alarm after a newer successful run.

## Operational tracks

Operational hardening proceeds along six tracks:

1. **Rail correctness:** invariant, idempotency, retry, reconciliation, refund,
   dispute, and correction proofs.
2. **Security:** identity and authorization, secret rotation, webhook abuse,
   journal custody, network boundaries, and live-mode admission.
3. **Release safety:** supply-chain verification, Flagger rollback, schema
   compatibility, and fail-closed customer-write control.
4. **Resilience:** replica loss, disk loss, node loss, provider outage,
   PostgreSQL recovery, R2 recovery, and full disaster recovery.
5. **Observability:** actionable metrics, logs, complete traces, durable audit
   evidence, and human delivery for every critical alert.
6. **Continuous verification:** browser and rail canaries, reconciliation,
   failure injection, and scheduled failover drills.

Customer transactions remain disabled until these tracks satisfy the
customer-write gates in [the TigerBeetle production
contract](tigerbeetle.md#customer-write-readiness).
