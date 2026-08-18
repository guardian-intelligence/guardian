-- Tool installations referenced by a restored process must share the same
-- generation as its workspace and process image. The zvol is sparse, so this
-- capacity costs only the blocks a workflow actually writes.

ALTER TABLE runner_classes
    ADD COLUMN tool_disk_bytes BIGINT NOT NULL DEFAULT 34359738368
        CHECK (tool_disk_bytes > 0);
