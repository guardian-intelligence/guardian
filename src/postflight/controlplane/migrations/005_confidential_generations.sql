-- The first confidential runner class has one fixed guest platform and a
-- paired process-state volume. A generation is usable for process restore
-- only when its CRIU artifact was reported at exit and confirmed again when
-- the host sealed the exact generation candidate.

ALTER TABLE runner_classes
    ADD COLUMN process_disk_bytes BIGINT NOT NULL DEFAULT 25769803776
        CHECK (process_disk_bytes > 0),
    ADD COLUMN confidential_technology TEXT NOT NULL DEFAULT '';

INSERT INTO runner_classes (
    class, cpu_cores, memory_bytes, disk_bytes, process_disk_bytes,
    confidential_technology
) VALUES (
    'postflight-4-ubuntu-24.04-github-confidential',
    4, 17179869184, 85899345920, 25769803776, 'sev-snp'
);

ALTER TABLE host_leases
    ADD COLUMN checkpoint_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN checkpoint_version TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_generations
    ADD COLUMN process_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN criu_version TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_generations
    ADD CONSTRAINT workspace_generations_process_pair CHECK (
        (process_digest = '' AND criu_version = '') OR
        (process_digest ~ '^sha256:[0-9a-f]{64}$' AND criu_version <> '')
    );
