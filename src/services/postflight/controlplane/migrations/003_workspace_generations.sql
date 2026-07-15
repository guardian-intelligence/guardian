-- 003_workspace_generations.sql — postflight-runner control plane, stage (c):
-- workspace scopes (the job-shape cache key and its current-generation
-- pointer), lease linkage for clone provenance and the promotion CAS, and
-- the seal/retention vocabulary on the generation catalog.

-- One scope = one workspace lineage. The key is every job-shape dimension
-- that changes what the workspace contains; without them, lint/test/build
-- would alternate ownership of one lineage and lose every promotion race.
CREATE TABLE workspace_scopes (
    scope_id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org                   TEXT NOT NULL CHECK (org <> ''),
    repo                  TEXT NOT NULL CHECK (repo <> ''),
    -- The branch whose lineage this scope caches: the head branch for
    -- branch-trust jobs, the TARGET branch for PR jobs (PR jobs read the
    -- target's generations; their writes are never promoted).
    scope_ref             TEXT NOT NULL DEFAULT '',
    workflow_path         TEXT NOT NULL DEFAULT '',
    job_name              TEXT NOT NULL DEFAULT '',
    matrix_key            TEXT NOT NULL DEFAULT '',
    runner_class          TEXT NOT NULL CHECK (runner_class <> ''),
    -- The pointer every new lease of this scope clones. Advanced only by
    -- the promotion CAS; NULL until the first green run seeds the lineage.
    current_generation_id TEXT,
    -- Residency of the current generation: a clone is ~free there and a
    -- full send anywhere else, so the slot claim prefers this host.
    home_host_id          TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_workspace_scopes_key
    ON workspace_scopes (org, repo, scope_ref, workflow_path, job_name, matrix_key, runner_class);

ALTER TABLE github_provider_demands
    ADD COLUMN workspace_scope_id UUID;

-- Set only when an API read persisted the completed status. Promotion gates
-- on this, not on observed_from_api_at: that column is sticky provenance
-- from any earlier read, so a completed webhook HINT landing after the
-- queued-time API read would otherwise masquerade as observed truth.
ALTER TABLE github_workflow_jobs
    ADD COLUMN terminal_observed_from_api_at TIMESTAMPTZ;

ALTER TABLE host_leases
    ADD COLUMN workspace_scope_id UUID,
    -- The scope pointer as read at claim, NULL when the scope had no
    -- generation. This exact value is the promotion CAS guard: the pointer
    -- only advances if nothing else advanced it since this lease was placed.
    ADD COLUMN observed_source_generation TEXT,
    ADD COLUMN seal_deadline_at TIMESTAMPTZ;

-- 'sealing': runner exited 0 on a trusted ref, the slot is already released,
-- and the host has been asked to seal the workspace as a generation.
ALTER TABLE host_leases DROP CONSTRAINT host_leases_state_check;
ALTER TABLE host_leases ADD CONSTRAINT host_leases_state_check CHECK (state IN
    ('allocating', 'assigned', 'ready', 'sealing', 'completed', 'failed', 'expired'));

DROP INDEX idx_host_leases_desired;
CREATE INDEX idx_host_leases_desired
    ON host_leases (host_id)
    WHERE state IN ('assigned', 'ready', 'sealing');

CREATE INDEX idx_host_leases_sealing_deadline
    ON host_leases (seal_deadline_at)
    WHERE state = 'sealing';

ALTER TABLE workspace_generations
    ADD COLUMN scope_id UUID,
    ADD COLUMN source_generation TEXT,
    -- Stamped when the host confirms the seal; a candidate is only
    -- promotable once this is set.
    ADD COLUMN sealed_at TIMESTAMPTZ;

-- 'discarded': the seal was orphaned or GitHub's conclusion was anything but
-- an unambiguous attempt-matching success. 'reapable': unreferenced and
-- cleared for the sync response's reap verb. 'reaped': the host's inventory
-- confirmed the dataset is gone.
ALTER TABLE workspace_generations DROP CONSTRAINT workspace_generations_state_check;
ALTER TABLE workspace_generations ADD CONSTRAINT workspace_generations_state_check CHECK (state IN
    ('candidate', 'committed', 'current', 'retained', 'discarded', 'reapable', 'reaped'));

CREATE INDEX idx_workspace_generations_state
    ON workspace_generations (state);
