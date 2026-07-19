CREATE TABLE payment_orders (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    provider text NOT NULL CHECK (provider = 'stripe'),
    provider_account_id text NOT NULL,
    currency text NOT NULL CHECK (currency = lower(currency) AND length(currency) = 3),
    amount_cents bigint NOT NULL CHECK (amount_cents > 0),
    synthetic boolean NOT NULL CHECK (synthetic),
    status text NOT NULL CHECK (
        status IN ('created', 'checkout_open', 'provider_paid', 'ledger_posted', 'failed')
    ),
    trace_id text NOT NULL CHECK (trace_id ~ '^[0-9a-f]{32}$'),
    stripe_checkout_session_id text UNIQUE,
    stripe_payment_intent_id text UNIQUE,
    stripe_charge_id text,
    stripe_balance_transaction_id text,
    failure_class text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    paid_at timestamptz,
    ledger_posted_at timestamptz
);

CREATE INDEX payment_orders_status_created_idx
    ON payment_orders (status, created_at);

CREATE TABLE provider_events (
    provider_account_id text NOT NULL,
    event_id text NOT NULL,
    event_type text NOT NULL,
    object_id text,
    api_version text,
    livemode boolean NOT NULL,
    payload jsonb NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    processing_started_at timestamptz,
    processed_at timestamptz,
    attempt_count integer NOT NULL DEFAULT 0,
    last_error_class text,
    PRIMARY KEY (provider_account_id, event_id)
);

CREATE INDEX provider_events_pending_idx
    ON provider_events (received_at)
    WHERE processed_at IS NULL;

CREATE TABLE provider_balance_transactions (
    provider_account_id text NOT NULL,
    balance_transaction_id text NOT NULL,
    source_id text,
    reporting_category text NOT NULL,
    transaction_type text NOT NULL,
    currency text NOT NULL,
    gross_cents bigint NOT NULL,
    fee_cents bigint NOT NULL,
    net_cents bigint NOT NULL,
    available_on timestamptz,
    provider_created_at timestamptz NOT NULL,
    raw jsonb NOT NULL,
    order_id text REFERENCES payment_orders(id),
    ledger_projected_at timestamptz,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_account_id, balance_transaction_id),
    CHECK (gross_cents - fee_cents = net_cents)
);

CREATE INDEX provider_balance_transactions_unmatched_idx
    ON provider_balance_transactions (first_seen_at)
    WHERE order_id IS NULL OR ledger_projected_at IS NULL;

CREATE TABLE ledger_accounts (
    registry_key text PRIMARY KEY,
    account_id text NOT NULL UNIQUE CHECK (account_id ~ '^[0-9a-f]{1,32}$'),
    ledger integer NOT NULL CHECK (ledger = 2),
    code integer NOT NULL,
    flags integer NOT NULL,
    user_data_128 text NOT NULL CHECK (user_data_128 ~ '^[0-9a-f]{1,32}$'),
    user_data_64 bigint NOT NULL,
    user_data_32 integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz
);

CREATE TABLE ledger_commands (
    command_key text PRIMARY KEY,
    order_id text NOT NULL REFERENCES payment_orders(id),
    correlation_id text NOT NULL CHECK (correlation_id ~ '^[0-9a-f]{1,32}$'),
    transfer_capture_id text NOT NULL CHECK (transfer_capture_id ~ '^[0-9a-f]{1,32}$'),
    transfer_fee_id text CHECK (transfer_fee_id IS NULL OR transfer_fee_id ~ '^[0-9a-f]{1,32}$'),
    transfer_grant_id text NOT NULL CHECK (transfer_grant_id ~ '^[0-9a-f]{1,32}$'),
    payload jsonb,
    intent_journaled_at timestamptz,
    tigerbeetle_accepted_at timestamptz,
    outcome_journaled_at timestamptz,
    result jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE payment_canary_runs (
    id text PRIMARY KEY,
    order_id text REFERENCES payment_orders(id),
    trace_id text NOT NULL CHECK (trace_id ~ '^[0-9a-f]{32}$'),
    lane text NOT NULL CHECK (lane IN ('rail', 'checkout', 'lifecycle', 'negative')),
    status text NOT NULL CHECK (status IN ('running', 'passed', 'failed')),
    failure_class text,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX payment_canary_runs_latest_idx
    ON payment_canary_runs (lane, started_at DESC);

CREATE TABLE payment_reconciler_state (
    provider_account_id text PRIMARY KEY,
    balance_cursor_created_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
    last_success_at timestamptz,
    last_error_class text
);
