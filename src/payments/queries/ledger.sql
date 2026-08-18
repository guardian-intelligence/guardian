-- name: EnsureLedgerAccount :one
INSERT INTO ledger_accounts (
    registry_key, account_id, ledger, code, flags,
    user_data_128, user_data_64, user_data_32
) VALUES ($1, $2, 2, $3, $4, $5, $6, $7)
ON CONFLICT (registry_key) DO UPDATE
SET registry_key = EXCLUDED.registry_key
RETURNING *;

-- name: MarkLedgerAccountAccepted :exec
UPDATE ledger_accounts
SET accepted_at = COALESCE(accepted_at, now())
WHERE registry_key = $1;

-- name: EnsureLedgerCommand :one
INSERT INTO ledger_commands (
    command_key, order_id, correlation_id, transfer_capture_id,
    transfer_fee_id, transfer_grant_id
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (command_key) DO UPDATE
SET command_key = EXCLUDED.command_key
RETURNING *;

-- name: SetLedgerCommandPayload :exec
UPDATE ledger_commands SET payload = $2 WHERE command_key = $1;

-- name: MarkLedgerCommandIntentJournaled :exec
UPDATE ledger_commands
SET intent_journaled_at = COALESCE(intent_journaled_at, now())
WHERE command_key = $1;

-- name: MarkLedgerCommandAccepted :exec
UPDATE ledger_commands
SET tigerbeetle_accepted_at = COALESCE(tigerbeetle_accepted_at, now()),
    result = $2
WHERE command_key = $1;

-- name: MarkLedgerCommandOutcomeJournaled :exec
UPDATE ledger_commands
SET outcome_journaled_at = COALESCE(outcome_journaled_at, now())
WHERE command_key = $1;

-- name: CountJournalIncomplete :one
SELECT count(*)::bigint
FROM ledger_commands
WHERE intent_journaled_at IS NULL
   OR tigerbeetle_accepted_at IS NULL
   OR outcome_journaled_at IS NULL;
