-- name: CreateOrder :one
INSERT INTO payment_orders (
    id, organization_id, provider, provider_account_id, currency, amount_cents,
    synthetic, status, trace_id
) VALUES (
    $1, $2, 'stripe', $3, $4, $5, true, 'created', $6
)
RETURNING *;

-- name: SetOrderCheckoutSession :one
UPDATE payment_orders
SET stripe_checkout_session_id = $2,
    status = 'checkout_open',
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetOrderPaymentIntent :one
UPDATE payment_orders
SET stripe_payment_intent_id = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkOrderProviderPaid :one
UPDATE payment_orders
SET stripe_payment_intent_id = $2,
    stripe_charge_id = $3,
    stripe_balance_transaction_id = $4,
    status = 'provider_paid',
    paid_at = COALESCE(paid_at, now()),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkOrderLedgerPosted :one
UPDATE payment_orders
SET status = 'ledger_posted',
    ledger_posted_at = COALESCE(ledger_posted_at, now()),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkOrderFailed :one
UPDATE payment_orders
SET status = 'failed',
    failure_class = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetOrder :one
SELECT * FROM payment_orders WHERE id = $1;

-- name: GetOrderByPaymentIntent :one
SELECT * FROM payment_orders WHERE stripe_payment_intent_id = $1;

-- name: GetOrderByBalanceTransaction :one
SELECT * FROM payment_orders WHERE stripe_balance_transaction_id = $1;

-- name: CountStaleOrders :one
SELECT count(*)::bigint
FROM payment_orders
WHERE status NOT IN ('ledger_posted', 'failed')
  AND created_at < now() - sqlc.arg(older_than)::interval;
