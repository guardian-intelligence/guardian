-- 002_hostd_scheduler.sql — postflight-runner control plane, stage (b):
-- host inventory, slot accounting, host leases, and the workspace
-- generation catalog behind the hostd sync exchange.
--
-- Extensibility rulings baked into these shapes: runner classes are data
-- rows (never enums or Go constants), lifecycle states are CHECK-validated
-- TEXT (a new state is a migration, not a type surgery), and the generation
-- catalog carries its eviction-policy inputs (last_used_at, bytes, pinned)
-- from day one even though eviction ships later.

CREATE TABLE runner_classes (
    class        TEXT PRIMARY KEY CHECK (class <> ''),
    cpu_cores    INTEGER NOT NULL CHECK (cpu_cores > 0),
    memory_bytes BIGINT NOT NULL CHECK (memory_bytes > 0),
    -- disk_bytes sizes the empty workspace volume on a generation-cache miss.
    disk_bytes   BIGINT NOT NULL CHECK (disk_bytes > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO runner_classes (class, cpu_cores, memory_bytes, disk_bytes)
VALUES ('postflight-4cpu-ubuntu-2404', 4, 17179869184, 85899345920);

-- Hosts, keyed by the self-reported host_id of the sync exchange.
-- Capability columns (arch, tee, and the offered classes in host_slots) are
-- the scheduler's future placement filters; sync fills what it observes.
CREATE TABLE hosts (
    host_id      TEXT PRIMARY KEY CHECK (host_id <> ''),
    boot_id      TEXT NOT NULL DEFAULT '',
    arch         TEXT NOT NULL DEFAULT '',
    tee          BOOLEAN NOT NULL DEFAULT false,
    last_sync_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-class slot accounting on one host. total/warm/used mirror the host's
-- own report (level-triggered, replaced every sync); reserved is the
-- control plane's claim counter, advanced only by the scheduler's CAS and
-- released exactly once per lease by the guarded terminal transitions.
-- reserved <= total is enforced by the claim query, not a CHECK, so a host
-- shrinking its totals mid-lease degrades to deadline expiry instead of
-- failing ingest.
CREATE TABLE host_slots (
    host_id    TEXT NOT NULL REFERENCES hosts (host_id) ON DELETE CASCADE,
    class      TEXT NOT NULL CHECK (class <> ''),
    total      INTEGER NOT NULL CHECK (total >= 0),
    warm       INTEGER NOT NULL DEFAULT 0,
    used       INTEGER NOT NULL DEFAULT 0,
    reserved   INTEGER NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, class)
);

CREATE INDEX idx_host_slots_free
    ON host_slots (class, reserved, host_id);

-- One lease = one runner execution on one host. Rows carry the full desired
-- spec hostd needs plus tenant identity, so the sync response is a straight
-- projection. States: allocating (no host yet) -> assigned (slot claimed,
-- JIT minted, in the host's desired set) -> ready (runner registered) ->
-- completed; failure exits are failed (a named cause) and expired (a
-- deadline sweep). Terminal leases leave the desired set, which is the ack
-- that lets hostd forget them.
CREATE TABLE host_leases (
    lease_id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    provider_job_id        BIGINT NOT NULL,
    execution_id           TEXT NOT NULL CHECK (execution_id <> ''),
    attempt_id             TEXT NOT NULL CHECK (attempt_id <> ''),
    org_id                 TEXT NOT NULL DEFAULT '',
    installation_id        BIGINT NOT NULL DEFAULT 0,
    repository_id          BIGINT NOT NULL DEFAULT 0,
    repository_full_name   TEXT NOT NULL DEFAULT '',
    runner_class           TEXT NOT NULL REFERENCES runner_classes (class),
    state                  TEXT NOT NULL CHECK (state IN
        ('allocating', 'assigned', 'ready', 'completed', 'failed', 'expired')),
    host_id                TEXT NOT NULL DEFAULT '',
    jit_config             TEXT NOT NULL DEFAULT '',
    workspace_generation   TEXT NOT NULL DEFAULT '',
    workspace_size_bytes   BIGINT NOT NULL DEFAULT 0,
    seal_generation        TEXT NOT NULL DEFAULT '',
    -- reported_state mirrors hostd's last lease report verbatim, for
    -- debugging drift between the two state machines.
    reported_state         TEXT NOT NULL DEFAULT '',
    exit_code              INTEGER,
    reason                 TEXT NOT NULL DEFAULT '',
    -- Per-state deadlines, stamped on entry: an allocating lease must find
    -- a slot fast (capacity is either free or it is not), an assigned lease
    -- must reach ready before the runner registration window closes.
    allocate_deadline_at   TIMESTAMPTZ NOT NULL,
    assignment_deadline_at TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One active lease per provider job; terminal leases free the job for a
-- future retry lease.
CREATE UNIQUE INDEX idx_host_leases_active_job
    ON host_leases (provider_job_id)
    WHERE state IN ('allocating', 'assigned', 'ready');

CREATE INDEX idx_host_leases_desired
    ON host_leases (host_id)
    WHERE state IN ('assigned', 'ready');

CREATE INDEX idx_host_leases_allocating
    ON host_leases (allocate_deadline_at)
    WHERE state = 'allocating';

CREATE INDEX idx_host_leases_assigned_deadline
    ON host_leases (assignment_deadline_at)
    WHERE state = 'assigned';

-- The workspace generation catalog. Generations are node-local (host_id is
-- residency, and the only copy); states follow the seal/promotion ladder:
-- candidate -> committed -> current, with retained (kept but demoted) and
-- reaped (ordered destroyed; the sync response's reap verb) as exits.
-- last_used_at / bytes / pinned are the eviction policy's inputs.
CREATE TABLE workspace_generations (
    generation   TEXT PRIMARY KEY CHECK (generation <> ''),
    host_id      TEXT NOT NULL DEFAULT '',
    runner_class TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL CHECK (state IN
        ('candidate', 'committed', 'current', 'retained', 'reaped')),
    bytes        BIGINT NOT NULL DEFAULT 0,
    pinned       BOOLEAN NOT NULL DEFAULT false,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workspace_generations_host
    ON workspace_generations (host_id, state);
