-- The scope key's deliberate invalidation lever, carried as data on the
-- runner class: at demand time the control plane cannot know which host or
-- image will serve the job, so the key takes the class's guest_arch and a
-- coarse operator-bumped workspace_epoch instead of any physical image
-- identity. Bumping the epoch re-keys every scope of the class — each job
-- shape resolves to a fresh lineage with no current generation, runs cold
-- once, and reseals; the abandoned lineages drain through retention/reap.

ALTER TABLE runner_classes
    ADD COLUMN guest_arch TEXT NOT NULL DEFAULT 'x86_64' CHECK (guest_arch <> ''),
    ADD COLUMN workspace_epoch TEXT NOT NULL DEFAULT 'epoch-1' CHECK (workspace_epoch <> '');

ALTER TABLE workspace_scopes
    ADD COLUMN guest_arch TEXT NOT NULL DEFAULT 'x86_64' CHECK (guest_arch <> ''),
    ADD COLUMN workspace_epoch TEXT NOT NULL DEFAULT 'epoch-1' CHECK (workspace_epoch <> '');

DROP INDEX idx_workspace_scopes_key;
CREATE UNIQUE INDEX idx_workspace_scopes_key
    ON workspace_scopes (org, repo, scope_ref, workflow_path, job_name, matrix_key,
        runner_class, guest_arch, workspace_epoch);
