-- Pool members exist before jobs. GitHub's local runner assignment creates an
-- immutable binding; durable generations remain independent of either VM or
-- runner identity.

DROP TABLE host_leases CASCADE;

ALTER TABLE github_workflow_jobs
    ADD COLUMN check_run_id BIGINT NOT NULL DEFAULT 0 CHECK (check_run_id >= 0);

CREATE UNIQUE INDEX idx_github_workflow_jobs_check_run
    ON github_workflow_jobs (check_run_id) WHERE check_run_id > 0;

ALTER TABLE host_slots
    DROP COLUMN warm,
    DROP COLUMN used,
    DROP COLUMN reserved,
    ADD COLUMN booting INTEGER NOT NULL DEFAULT 0 CHECK (booting >= 0),
    ADD COLUMN listening INTEGER NOT NULL DEFAULT 0 CHECK (listening >= 0),
    ADD COLUMN busy INTEGER NOT NULL DEFAULT 0 CHECK (busy >= 0);

CREATE TABLE runner_pools (
    pool_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           TEXT NOT NULL CHECK (org_id <> ''),
    installation_id  BIGINT NOT NULL CHECK (installation_id > 0),
    runner_class     TEXT NOT NULL REFERENCES runner_classes (class),
    desired_count    INTEGER NOT NULL DEFAULT 0 CHECK (desired_count >= 0),
    enabled          BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, runner_class)
);

CREATE TABLE runner_pool_members (
    member_id        TEXT PRIMARY KEY CHECK (member_id <> ''),
    host_id          TEXT NOT NULL REFERENCES hosts (host_id) ON DELETE CASCADE,
    vm_id            TEXT NOT NULL CHECK (vm_id <> ''),
    pool_id          UUID REFERENCES runner_pools (pool_id),
    runner_name      TEXT NOT NULL DEFAULT '',
    runner_class     TEXT NOT NULL REFERENCES runner_classes (class),
    image            TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL CHECK (state IN
        ('provisioning', 'warm', 'preparing', 'listening', 'assigned',
         'rendezvous', 'running', 'recycling', 'lost')),
    jit_config       TEXT NOT NULL DEFAULT '',
    reported_reason  TEXT NOT NULL DEFAULT '',
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (host_id, vm_id),
    CHECK ((pool_id IS NULL AND runner_name = '' AND jit_config = '') OR pool_id IS NOT NULL)
);

CREATE UNIQUE INDEX idx_runner_pool_members_runner
    ON runner_pool_members (runner_name) WHERE runner_name <> '';
CREATE INDEX idx_runner_pool_members_pool_state
    ON runner_pool_members (pool_id, state);

CREATE TABLE github_job_intents (
    provider_job_id      BIGINT PRIMARY KEY REFERENCES github_workflow_jobs (provider_job_id) ON DELETE CASCADE,
    runner_class         TEXT NOT NULL REFERENCES runner_classes (class),
    repository_full_name TEXT NOT NULL CHECK (repository_full_name <> ''),
    provider_run_id      BIGINT NOT NULL CHECK (provider_run_id > 0),
    provider_run_attempt BIGINT NOT NULL CHECK (provider_run_attempt > 0),
    job_display_name     TEXT NOT NULL CHECK (job_display_name <> ''),
    check_run_id         BIGINT NOT NULL CHECK (check_run_id > 0),
    request_id           TEXT NOT NULL DEFAULT '',
    protocol_job_id      TEXT NOT NULL DEFAULT '',
    state                TEXT NOT NULL CHECK (state IN
        ('queued', 'observed', 'bound', 'running', 'completed', 'requeued', 'failed_closed')),
    available_unix_ns    BIGINT NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_github_job_intents_request
    ON github_job_intents (request_id) WHERE request_id <> '';
CREATE INDEX idx_github_job_intents_match
    ON github_job_intents (check_run_id)
    WHERE state IN ('queued', 'observed', 'requeued');

CREATE TABLE runner_job_assignments (
    assignment_id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id           TEXT NOT NULL REFERENCES runner_pool_members (member_id),
    provider_job_id     BIGINT NOT NULL REFERENCES github_job_intents (provider_job_id),
    host_id             TEXT NOT NULL REFERENCES hosts (host_id),
    request_id          TEXT NOT NULL CHECK (request_id <> ''),
    protocol_job_id     TEXT NOT NULL CHECK (protocol_job_id <> ''),
    check_run_id        BIGINT NOT NULL CHECK (check_run_id > 0),
    runner_name         TEXT NOT NULL CHECK (runner_name <> ''),
    job_display_name    TEXT NOT NULL CHECK (job_display_name <> ''),
    run_id              TEXT NOT NULL CHECK (run_id <> ''),
    run_attempt         INTEGER NOT NULL CHECK (run_attempt > 0),
    repository          TEXT NOT NULL CHECK (repository <> ''),
    workflow_job        TEXT NOT NULL CHECK (workflow_job <> ''),
    state               TEXT NOT NULL CHECK (state IN
        ('observed', 'binding', 'authorizing', 'running', 'exited',
         'sealing', 'sealed', 'completed', 'requeued', 'failed_closed')),
    workspace_scope_id  UUID REFERENCES workspace_scopes (scope_id),
    source_generation   TEXT NOT NULL DEFAULT '',
    seal_generation     TEXT NOT NULL DEFAULT '',
    checkpoint_digest   TEXT NOT NULL DEFAULT '',
    checkpoint_version  TEXT NOT NULL DEFAULT '',
    restore_outcome     TEXT NOT NULL DEFAULT '',
    restore_failure_class TEXT NOT NULL DEFAULT '',
    restore_failure_code  TEXT NOT NULL DEFAULT '',
    process_invalidated BOOLEAN NOT NULL DEFAULT false,
    exit_code           INTEGER,
    reason              TEXT NOT NULL DEFAULT '',
    timing_json         JSONB NOT NULL DEFAULT '[]'::jsonb,
    seal_deadline_at    TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (member_id),
    CHECK ((checkpoint_digest = '' AND checkpoint_version = '') OR
           (checkpoint_digest ~ '^sha256:[0-9a-f]{64}$' AND checkpoint_version <> ''))
);

CREATE INDEX idx_runner_job_assignments_host
    ON runner_job_assignments (host_id, state);
CREATE UNIQUE INDEX idx_runner_job_assignments_active_job
    ON runner_job_assignments (provider_job_id)
    WHERE state IN ('observed', 'binding', 'authorizing', 'running', 'exited', 'sealing');
CREATE INDEX idx_runner_job_assignments_request
    ON runner_job_assignments (request_id);
CREATE INDEX idx_runner_job_assignments_protocol_job
    ON runner_job_assignments (protocol_job_id);
CREATE INDEX idx_runner_job_assignments_sealing
    ON runner_job_assignments (seal_deadline_at) WHERE state = 'sealing';
