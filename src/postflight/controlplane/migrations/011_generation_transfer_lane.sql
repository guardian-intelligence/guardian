-- The migrate-on-miss transfer lane. A host that serves its sealed
-- generations over the private transfer VLAN advertises the origin peers
-- reach it at; a host with an empty origin never has transfers routed at it
-- and its off-host jobs run cold. Assignments record the routing they were
-- issued with and the assigned host's transfer evidence, mirroring how
-- restore evidence lives on the assignment row.

ALTER TABLE hosts
    ADD COLUMN transfer_origin TEXT NOT NULL DEFAULT '';

ALTER TABLE runner_job_assignments
    ADD COLUMN transfer_origin      TEXT NOT NULL DEFAULT '',
    ADD COLUMN transfer_base        TEXT NOT NULL DEFAULT '',
    ADD COLUMN transfer_outcome     TEXT NOT NULL DEFAULT '',
    ADD COLUMN transfer_bytes       BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN transfer_millis      BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN transfer_incremental BOOLEAN NOT NULL DEFAULT false;
