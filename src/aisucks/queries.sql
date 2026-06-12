-- name: InsertReport :one
INSERT INTO reports (share_url, provider, model, parser_version, status)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (share_url) DO NOTHING
RETURNING id;

-- name: InsertTurns :copyfrom
INSERT INTO turns (report_id, idx, role, content)
VALUES ($1, $2, $3, $4);
