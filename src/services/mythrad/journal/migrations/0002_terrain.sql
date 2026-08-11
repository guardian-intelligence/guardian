-- Terrain artifacts are content-addressed and immutable: one row per
-- distinct blob, shared by every park born from it. uint64 identities are
-- stored as their two's-complement bigint image (the tick/wh convention).
-- Snapshots record which terrain their state was captured on so a restore
-- can load the matching blob first.
CREATE TABLE park_terrain (
    terrain_id bigint      NOT NULL,
    schema     integer     NOT NULL,
    blob       bytea       NOT NULL,
    wall_ts    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (terrain_id)
);

ALTER TABLE park_snapshots ADD COLUMN terrain_id bigint NOT NULL DEFAULT 0;
