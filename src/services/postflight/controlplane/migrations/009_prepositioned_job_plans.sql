-- Freeze the durable-generation choice while a provider job is still queued.
-- hostd receives this plan before GitHub selects a listener and uses the same
-- identity when it later reports the acquired assignment.

ALTER TABLE github_provider_demands
    ADD COLUMN plan_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN source_generation TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_github_provider_demands_plan
    ON github_provider_demands (plan_id);

