-- 001_initial.sql — postflight-runner control plane, stage (a).
--
-- Table shapes are ported from postflight's github-integration-service collapsed
-- baseline migration. Deliberate stage-(a) omissions: installations /
-- repositories mirror tables (single-tenant, installation pinned in config),
-- github_runner_instances and job_shapes (stage b), idempotency records (no
-- customer mutations in this slice).

CREATE TABLE github_webhook_deliveries (
    delivery_id              TEXT PRIMARY KEY CHECK (delivery_id <> ''),
    event_name               TEXT NOT NULL CHECK (event_name <> ''),
    action                   TEXT NOT NULL DEFAULT '',
    state                    TEXT NOT NULL CHECK (state <> ''),
    primary_problem_type     TEXT NOT NULL DEFAULT '',
    primary_problem_code     TEXT NOT NULL DEFAULT '',
    primary_problem_title    TEXT NOT NULL DEFAULT '',
    primary_problem_detail   TEXT NOT NULL DEFAULT '',
    primary_problem_docs_url TEXT NOT NULL DEFAULT '',
    primary_problem_status   INTEGER NOT NULL DEFAULT 0 CHECK (primary_problem_status BETWEEN 0 AND 599),
    problem_count            INTEGER NOT NULL DEFAULT 0 CHECK (problem_count >= 0),
    payload_sha256           TEXT NOT NULL CHECK (payload_sha256 <> ''),
    payload_json             JSONB NOT NULL,
    attempt_count            INTEGER NOT NULL DEFAULT 0,
    -- Denormalized envelope: failure terminalization must not re-parse
    -- payload_json.
    provider_installation_id BIGINT NOT NULL DEFAULT 0,
    provider_repository_id   BIGINT NOT NULL DEFAULT 0,
    repository_full_name     TEXT NOT NULL DEFAULT '',
    provider_run_id          BIGINT NOT NULL DEFAULT 0,
    provider_run_attempt     BIGINT NOT NULL DEFAULT 0,
    provider_job_id          BIGINT NOT NULL DEFAULT 0,
    received_at              TIMESTAMPTZ NOT NULL,
    verified_at              TIMESTAMPTZ NOT NULL,
    next_attempt_at          TIMESTAMPTZ,
    processing_started_at    TIMESTAMPTZ,
    processed_at             TIMESTAMPTZ,
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_github_webhook_deliveries_pending
    ON github_webhook_deliveries (next_attempt_at NULLS FIRST, received_at)
    WHERE state IN ('accepted', 'retryable');

CREATE TABLE github_webhook_delivery_problems (
    delivery_id  TEXT NOT NULL REFERENCES github_webhook_deliveries (delivery_id) ON DELETE CASCADE,
    problem_seq  INTEGER NOT NULL CHECK (problem_seq > 0),
    phase        TEXT NOT NULL CHECK (phase <> ''),
    problem_type TEXT NOT NULL CHECK (problem_type <> ''),
    problem_code TEXT NOT NULL CHECK (problem_code <> ''),
    title        TEXT NOT NULL CHECK (title <> ''),
    detail       TEXT NOT NULL DEFAULT '',
    docs_url     TEXT NOT NULL DEFAULT '',
    status       INTEGER NOT NULL DEFAULT 0 CHECK (status BETWEEN 0 AND 599),
    retryable    BOOLEAN NOT NULL DEFAULT false,
    pointer      TEXT NOT NULL DEFAULT '',
    observed_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (delivery_id, problem_seq)
);

CREATE TABLE github_workflow_jobs (
    provider_job_id        BIGINT PRIMARY KEY,
    provider_run_id        BIGINT NOT NULL DEFAULT 0,
    provider_run_attempt   BIGINT NOT NULL DEFAULT 0,
    provider_repository_id BIGINT NOT NULL DEFAULT 0,
    repository_full_name   TEXT NOT NULL DEFAULT '',
    name                   TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL DEFAULT '',
    conclusion             TEXT NOT NULL DEFAULT '',
    labels_json            JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Resolved in Go (payload-order first prefix match) at every persist, so
    -- the reconciliation sweeper reads the exact class the webhook path
    -- resolved. postflight resolved the sweeper's class separately in SQL
    -- (lexicographically first prefixed label), which could disagree with the
    -- payload-order rule on multi-label jobs.
    runner_class           TEXT NOT NULL DEFAULT '',
    runner_id              BIGINT NOT NULL DEFAULT 0,
    runner_name            TEXT NOT NULL DEFAULT '',
    head_sha               TEXT NOT NULL DEFAULT '',
    head_branch            TEXT NOT NULL DEFAULT '',
    workflow_name          TEXT NOT NULL DEFAULT '',
    -- NULL = not yet resolved from the API; 0 = the run has no associated PR
    -- (e.g. push to main); > 0 = the PR the comment engine renders into.
    pr_number              BIGINT,
    started_at             TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ,
    -- Set only on API reads: records webhook-hint vs API-truth provenance.
    observed_from_api_at   TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_github_workflow_jobs_queued
    ON github_workflow_jobs (provider_repository_id, runner_class, provider_job_id)
    WHERE status = 'queued' AND runner_class <> '';

CREATE INDEX idx_github_workflow_jobs_pr
    ON github_workflow_jobs (provider_repository_id, pr_number)
    WHERE pr_number IS NOT NULL AND pr_number > 0;

CREATE TABLE github_provider_demands (
    demand_id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_job_id          BIGINT NOT NULL UNIQUE REFERENCES github_workflow_jobs (provider_job_id) ON DELETE CASCADE,
    provider_repository_id   BIGINT NOT NULL DEFAULT 0,
    repository_full_name     TEXT NOT NULL DEFAULT '',
    provider_run_id          BIGINT NOT NULL DEFAULT 0,
    provider_run_attempt     BIGINT NOT NULL DEFAULT 0,
    trust_class              TEXT NOT NULL DEFAULT '',
    runner_class             TEXT NOT NULL DEFAULT '',
    -- Full vocabulary: demand_recorded -> capacity_requested -> assigned ->
    -- completed, failure states capacity_failed / jit_failed / sandbox_failed.
    -- Stage (a) only ever writes demand_recorded and capacity_failed.
    state                    TEXT NOT NULL CHECK (state <> ''),
    primary_problem_type     TEXT NOT NULL DEFAULT '',
    primary_problem_code     TEXT NOT NULL DEFAULT '',
    primary_problem_title    TEXT NOT NULL DEFAULT '',
    primary_problem_detail   TEXT NOT NULL DEFAULT '',
    primary_problem_docs_url TEXT NOT NULL DEFAULT '',
    primary_problem_status   INTEGER NOT NULL DEFAULT 0 CHECK (primary_problem_status BETWEEN 0 AND 599),
    problem_count            INTEGER NOT NULL DEFAULT 0 CHECK (problem_count >= 0),
    last_delivery_id         TEXT NOT NULL DEFAULT '',
    claimed_at               TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE github_provider_demand_problems (
    provider_job_id BIGINT NOT NULL REFERENCES github_provider_demands (provider_job_id) ON DELETE CASCADE,
    problem_seq     INTEGER NOT NULL CHECK (problem_seq > 0),
    phase           TEXT NOT NULL CHECK (phase <> ''),
    problem_type    TEXT NOT NULL CHECK (problem_type <> ''),
    problem_code    TEXT NOT NULL CHECK (problem_code <> ''),
    title           TEXT NOT NULL CHECK (title <> ''),
    detail          TEXT NOT NULL DEFAULT '',
    docs_url        TEXT NOT NULL DEFAULT '',
    status          INTEGER NOT NULL DEFAULT 0 CHECK (status BETWEEN 0 AND 599),
    retryable       BOOLEAN NOT NULL DEFAULT false,
    pointer         TEXT NOT NULL DEFAULT '',
    observed_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (provider_job_id, problem_seq)
);

-- Observed provider truth, written only from API reads. origin (which job
-- caused capacity) vs actual (what GitHub assigned) are distinct concepts;
-- this table is the "actual" half and starts accruing displacement data now,
-- before stage (b) adds runner instances.
CREATE TABLE github_job_assignments (
    provider_job_id BIGINT PRIMARY KEY REFERENCES github_workflow_jobs (provider_job_id) ON DELETE CASCADE,
    runner_name     TEXT NOT NULL CHECK (runner_name <> ''),
    runner_id       BIGINT NOT NULL DEFAULT 0,
    observed_from   TEXT NOT NULL,
    delivery_id     TEXT NOT NULL DEFAULT '',
    observed_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_github_job_assignments_runner_name
    ON github_job_assignments (runner_name);

CREATE INDEX idx_github_job_assignments_runner_id
    ON github_job_assignments (runner_id)
    WHERE runner_id <> 0;

-- One comment per PR; the comment loop owns this table. Comment delivery is
-- decoupled from delivery processing: failures back off here, never in the
-- webhook ledger.
CREATE TABLE pr_comment_state (
    provider_repository_id BIGINT NOT NULL,
    pr_number              BIGINT NOT NULL CHECK (pr_number > 0),
    repository_full_name   TEXT NOT NULL DEFAULT '',
    provider_comment_id    BIGINT NOT NULL DEFAULT 0, -- 0 = not yet posted
    last_rendered_sha256   TEXT NOT NULL DEFAULT '',
    dirty                  BOOLEAN NOT NULL DEFAULT true,
    attempt_count          INTEGER NOT NULL DEFAULT 0,
    next_attempt_at        TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_repository_id, pr_number)
);

CREATE INDEX idx_pr_comment_state_dirty
    ON pr_comment_state (updated_at)
    WHERE dirty;
