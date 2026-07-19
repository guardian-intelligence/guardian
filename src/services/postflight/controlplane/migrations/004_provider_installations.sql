ALTER TABLE github_workflow_jobs
    ADD COLUMN IF NOT EXISTS provider_installation_id BIGINT;

UPDATE github_workflow_jobs AS job
SET provider_installation_id = (
    SELECT delivery.provider_installation_id
    FROM github_webhook_deliveries AS delivery
    WHERE delivery.provider_installation_id > 0
      AND (
          delivery.provider_job_id = job.provider_job_id
          OR delivery.provider_repository_id = job.provider_repository_id
      )
    ORDER BY
        (delivery.provider_job_id = job.provider_job_id) DESC,
        delivery.received_at DESC
    LIMIT 1
)
WHERE job.provider_installation_id IS NULL;

ALTER TABLE github_provider_demands
    ADD COLUMN IF NOT EXISTS provider_installation_id BIGINT;

UPDATE github_provider_demands AS demand
SET provider_installation_id = job.provider_installation_id
FROM github_workflow_jobs AS job
WHERE job.provider_job_id = demand.provider_job_id
  AND demand.provider_installation_id IS NULL;

ALTER TABLE pr_comment_state
    ADD COLUMN IF NOT EXISTS provider_installation_id BIGINT;

UPDATE pr_comment_state AS comment
SET provider_installation_id = (
    SELECT job.provider_installation_id
    FROM github_workflow_jobs AS job
    WHERE job.provider_repository_id = comment.provider_repository_id
      AND job.provider_installation_id > 0
    ORDER BY job.updated_at DESC
    LIMIT 1
)
WHERE comment.provider_installation_id IS NULL;

ALTER TABLE github_workflow_jobs
    ALTER COLUMN provider_installation_id SET NOT NULL;
ALTER TABLE github_provider_demands
    ALTER COLUMN provider_installation_id SET NOT NULL;
ALTER TABLE pr_comment_state
    ALTER COLUMN provider_installation_id SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'github_workflow_jobs_installation_positive'
    ) THEN
        ALTER TABLE github_workflow_jobs
            ADD CONSTRAINT github_workflow_jobs_installation_positive
            CHECK (provider_installation_id > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'github_provider_demands_installation_positive'
    ) THEN
        ALTER TABLE github_provider_demands
            ADD CONSTRAINT github_provider_demands_installation_positive
            CHECK (provider_installation_id > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'pr_comment_state_installation_positive'
    ) THEN
        ALTER TABLE pr_comment_state
            ADD CONSTRAINT pr_comment_state_installation_positive
            CHECK (provider_installation_id > 0);
    END IF;
END
$$;
