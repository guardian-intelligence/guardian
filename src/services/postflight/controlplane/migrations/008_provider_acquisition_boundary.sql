-- A local assignment is observed only after GitHub's acquirejob commit point.
-- The provider exposes no operation that releases that job message to another
-- listener, so post-acquisition loss must fail closed instead of claiming a
-- requeue that cannot occur.

WITH affected AS (
    SELECT d.provider_job_id,
           COALESCE(MAX(p.problem_seq), 0) + 1 AS problem_seq
    FROM github_provider_demands d
    JOIN github_job_intents i USING (provider_job_id)
    LEFT JOIN github_provider_demand_problems p USING (provider_job_id)
    WHERE i.state = 'requeued'
      AND d.state IN ('demand_recorded', 'capacity_requested', 'assigned')
    GROUP BY d.provider_job_id
)
INSERT INTO github_provider_demand_problems (
    provider_job_id, problem_seq, phase, problem_type, problem_code,
    title, detail, docs_url, status, retryable, pointer, observed_at
)
SELECT provider_job_id, problem_seq, 'processing',
       'urn:guardian:postflight-runner:problem:assignment.sandbox_failed',
       'assignment.sandbox_failed', 'host reported the assignment failed',
       'provider-acquired assignment cannot be requeued', '', 0, false, '', now()
FROM affected;

UPDATE github_provider_demands d
SET state = 'sandbox_failed',
    primary_problem_type = CASE WHEN problem_count = 0 THEN
        'urn:guardian:postflight-runner:problem:assignment.sandbox_failed'
        ELSE primary_problem_type END,
    primary_problem_code = CASE WHEN problem_count = 0 THEN
        'assignment.sandbox_failed' ELSE primary_problem_code END,
    primary_problem_title = CASE WHEN problem_count = 0 THEN
        'host reported the assignment failed' ELSE primary_problem_title END,
    primary_problem_detail = CASE WHEN problem_count = 0 THEN
        'provider-acquired assignment cannot be requeued'
        ELSE primary_problem_detail END,
    problem_count = problem_count + 1,
    updated_at = now()
FROM github_job_intents i
WHERE i.provider_job_id = d.provider_job_id
  AND i.state = 'requeued'
  AND d.state IN ('demand_recorded', 'capacity_requested', 'assigned');

UPDATE runner_job_assignments
SET state = 'failed_closed',
    reason = CASE WHEN reason = '' THEN 'provider-acquired assignment cannot be requeued' ELSE reason END,
    updated_at = now()
WHERE state = 'requeued';

UPDATE github_job_intents
SET state = 'failed_closed', updated_at = now()
WHERE state = 'requeued';

ALTER TABLE github_job_intents
    DROP CONSTRAINT github_job_intents_state_check,
    ADD CONSTRAINT github_job_intents_state_check CHECK (state IN
        ('queued', 'observed', 'bound', 'running', 'completed', 'failed_closed'));

ALTER TABLE runner_job_assignments
    DROP CONSTRAINT runner_job_assignments_state_check,
    ADD CONSTRAINT runner_job_assignments_state_check CHECK (state IN
        ('observed', 'binding', 'authorizing', 'running', 'exited',
         'sealing', 'sealed', 'completed', 'failed_closed'));

DROP INDEX idx_github_job_intents_match;
CREATE INDEX idx_github_job_intents_match
    ON github_job_intents (check_run_id)
    WHERE state IN ('queued', 'observed');
