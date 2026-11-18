-- M3: one headless run (design 8.2, 8.3, 11.2, 12.4, 12.10).

-- What a subtask is, and why it waits. A subtask with no ticket says what it belongs to by its
-- scope; M3's are of the fixture project.
ALTER TABLE subtasks
    ADD COLUMN title       text        NOT NULL DEFAULT '',
    ADD COLUMN wait_for    text,
    ADD COLUMN wait_until  timestamptz,
    ADD COLUMN stops       integer     NOT NULL DEFAULT 0,
    ADD COLUMN updated_at  timestamptz NOT NULL DEFAULT now();

-- What the monitor shows of a run (8.1), and how it was started and continued (12.4, 12.10).
ALTER TABLE runs
    ADD COLUMN turn            integer     NOT NULL DEFAULT 0,
    ADD COLUMN events          bigint      NOT NULL DEFAULT 0,
    ADD COLUMN tool_steps      integer     NOT NULL DEFAULT 0,
    ADD COLUMN last_event_at   timestamptz,
    ADD COLUMN last_action     text,
    ADD COLUMN warnings        jsonb       NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN stop_detail     text,
    ADD COLUMN exit_code       integer,
    ADD COLUMN way             text        NOT NULL DEFAULT 'new'
        CHECK (way IN ('new', 'same', 'files', 'rebuilt')),
    ADD COLUMN continues       uuid REFERENCES runs (id),
    ADD COLUMN base_ref        text,
    ADD COLUMN wip_ref         text,
    ADD COLUMN branch          text,
    ADD COLUMN timeout_s       integer     NOT NULL DEFAULT 3600,
    ADD COLUMN max_turns       integer     NOT NULL DEFAULT 200,
    ADD COLUMN tools           boolean     NOT NULL DEFAULT true,
    ADD COLUMN stopping        text;
-- A run's cost is an estimate at list prices, and a short call costs a fraction of a cent.
ALTER TABLE runs ALTER COLUMN cost_usd TYPE numeric(14, 6);
CREATE INDEX runs_live ON runs (state) WHERE state <> 'ended';

-- The last totals the CLI reported for the conversation, so a run's own use is the difference
-- (12.10). They belong to the CLI's id of the conversation, and start again when it is rebuilt.
ALTER TABLE conversations
    ADD COLUMN cli_totals  jsonb       NOT NULL DEFAULT '{}'::jsonb;

-- How far the backend has read each turn's spool (12.4). The events of a line and the line's place
-- here are written in one transaction, so every line becomes one row exactly once, also across a
-- restart.
CREATE TABLE run_spools (
    run         uuid        NOT NULL REFERENCES runs (id),
    turn        integer     NOT NULL,
    "offset"    bigint      NOT NULL DEFAULT 0,
    lines       bigint      NOT NULL DEFAULT 0,
    done        boolean     NOT NULL DEFAULT false,
    PRIMARY KEY (run, turn)
);

-- The repositories the git endpoint serves (6.1): in M3 one, the fixture's quarantine repository.
CREATE TABLE git_repos (
    name        text PRIMARY KEY,
    project     uuid,
    seeded_from text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
