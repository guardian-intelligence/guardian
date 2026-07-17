-- name: InsertProviderEvent :one
INSERT INTO provider_events (
    provider_account_id, event_id, event_type, object_id, api_version,
    livemode, payload
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider_account_id, event_id) DO NOTHING
RETURNING *;

-- name: ClaimProviderEvent :one
UPDATE provider_events
SET processing_started_at = now(),
    attempt_count = attempt_count + 1
WHERE (provider_account_id, event_id) = (
    SELECT provider_account_id, event_id
    FROM provider_events
    WHERE processed_at IS NULL
      AND (
        processing_started_at IS NULL
        OR processing_started_at < now() - interval '2 minutes'
      )
    ORDER BY received_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *;

-- name: MarkProviderEventProcessed :exec
UPDATE provider_events
SET processed_at = now(),
    last_error_class = NULL
WHERE provider_account_id = $1 AND event_id = $2;

-- name: MarkProviderEventRetryable :exec
UPDATE provider_events
SET processing_started_at = NULL,
    last_error_class = $3
WHERE provider_account_id = $1 AND event_id = $2;

-- name: CountPendingProviderEvents :one
SELECT count(*)::bigint FROM provider_events WHERE processed_at IS NULL;

-- name: OldestPendingProviderEventAgeSeconds :one
SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(received_at)), 0)::bigint
FROM provider_events
WHERE processed_at IS NULL;

-- name: UpsertBalanceTransaction :one
INSERT INTO provider_balance_transactions (
    provider_account_id, balance_transaction_id, source_id,
    reporting_category, transaction_type, currency, gross_cents, fee_cents,
    net_cents, available_on, provider_created_at, raw, order_id,
    ledger_projected_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
)
ON CONFLICT (provider_account_id, balance_transaction_id) DO UPDATE
SET source_id = EXCLUDED.source_id,
    reporting_category = EXCLUDED.reporting_category,
    transaction_type = EXCLUDED.transaction_type,
    currency = EXCLUDED.currency,
    gross_cents = EXCLUDED.gross_cents,
    fee_cents = EXCLUDED.fee_cents,
    net_cents = EXCLUDED.net_cents,
    available_on = EXCLUDED.available_on,
    raw = EXCLUDED.raw,
    order_id = COALESCE(
        provider_balance_transactions.order_id,
        EXCLUDED.order_id
    ),
    ledger_projected_at = COALESCE(
        provider_balance_transactions.ledger_projected_at,
        EXCLUDED.ledger_projected_at
    ),
    last_seen_at = now()
RETURNING *;

-- name: CountUnmatchedBalanceTransactions :one
SELECT count(*)::bigint
FROM provider_balance_transactions
WHERE (order_id IS NULL OR ledger_projected_at IS NULL)
  AND first_seen_at < now() - interval '15 minutes';
