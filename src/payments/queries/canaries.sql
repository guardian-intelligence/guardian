-- name: StartCanaryRun :one
INSERT INTO payment_canary_runs (id, trace_id, lane, status)
VALUES ($1, $2, $3, 'running')
RETURNING *;

-- name: AttachCanaryOrder :exec
UPDATE payment_canary_runs SET order_id = $2 WHERE id = $1;

-- name: GetCanaryRun :one
SELECT * FROM payment_canary_runs WHERE id = $1;

-- name: CompleteCanaryRun :one
UPDATE payment_canary_runs
SET status = $2,
    failure_class = $3,
    completed_at = now()
WHERE id = $1 AND status = 'running'
RETURNING *;

-- name: LatestCanaryRuns :many
SELECT DISTINCT ON (lane) *
FROM payment_canary_runs
ORDER BY lane, started_at DESC;

-- name: CountCanaryFailures :one
SELECT count(*)::bigint
FROM payment_canary_runs
WHERE status = 'failed'
  AND started_at > now() - sqlc.arg(interval_window)::interval;
