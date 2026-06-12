-- v0 collect-only schema. Additive-only discipline applies from here on:
-- the previous binary must run against the current schema (rollback path).
CREATE TABLE reports (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    share_url      text NOT NULL UNIQUE,
    provider       text NOT NULL,
    model          text NOT NULL DEFAULT '',
    parser_version int  NOT NULL,
    -- 'stored': transcript extracted and saved.
    -- 'parse_failed': link was live but the adapter couldn't extract; the
    -- URL is kept so a fixed adapter can re-fetch later. No raw HTML is
    -- ever persisted.
    status         text NOT NULL CHECK (status IN ('stored', 'parse_failed')),
    submitted_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE turns (
    report_id bigint NOT NULL REFERENCES reports (id),
    idx       int    NOT NULL,
    role      text   NOT NULL,
    content   text   NOT NULL,
    PRIMARY KEY (report_id, idx)
);
