-- The park journal: ordered events are the truth, snapshots bound replay
-- time (docs/netcode.md). uint64 sim values (tick, intent_id, wh) are
-- stored as their two's-complement bigint image. wall_ts is observability
-- only and is never replayed.
CREATE TABLE park_events (
    park_id   bigint      NOT NULL,
    seq       bigint      NOT NULL,
    tick      bigint      NOT NULL,
    epoch     integer     NOT NULL,
    kind      smallint    NOT NULL,
    actor     text        NOT NULL,
    intent_id bigint      NOT NULL,
    payload   bytea       NOT NULL,
    wall_ts   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (park_id, seq)
);

CREATE TABLE park_snapshots (
    park_id bigint      NOT NULL,
    seq     bigint      NOT NULL,
    tick    bigint      NOT NULL,
    epoch   integer     NOT NULL,
    wh      bigint      NOT NULL,
    state   bytea       NOT NULL,
    wall_ts timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (park_id, seq)
);
