-- The first schema (design 15.3): only what M1 to M3 use. Each later milestone adds its tables as a
-- numbered change. Tables are owned by crew_migrate; the server's role crew may read and write their
-- rows, and crew_read may only read (the default privileges are set when the database is made).
-- Every row that belongs to a project carries it. The projects table comes with M4, which adds the
-- foreign keys to it.

-- Both sets of settings (14.1): the machine's (project is null) and each project's. The document
-- holds only what the operator set; the defaults come from the code.
CREATE TABLE settings (
    id          bigserial PRIMARY KEY,
    project     uuid,
    doc         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  text        NOT NULL
);
CREATE UNIQUE INDEX settings_one_per_scope ON settings ((coalesce(project::text, 'machine')));

-- Every credential from Setup, encrypted with the key file that is not in the database (15.3).
CREATE TABLE credentials (
    id          text PRIMARY KEY,
    kind        text        NOT NULL,
    account     text,
    nonce       bytea       NOT NULL,
    sealed      bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Every change to a ticket, a record, or a setting, written in the same transaction (6.4).
-- Rows are only added: the server's role has no right to change or remove one.
CREATE TABLE changes (
    seq         bigserial PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    who         text        NOT NULL,
    tbl         text        NOT NULL,
    row_key     text        NOT NULL,
    before      jsonb,
    after       jsonb
);
CREATE INDEX changes_by_row ON changes (tbl, row_key, seq);

-- Pending work. One pending job per key, so a reconcile that runs twice starts a thing once.
CREATE TABLE jobs (
    id          bigserial PRIMARY KEY,
    kind        text        NOT NULL,
    target      text,
    key         text        NOT NULL,
    class       text,
    not_before  timestamptz NOT NULL DEFAULT now(),
    state       text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'done', 'failed')),
    tries       integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX jobs_one_pending_per_key ON jobs (key) WHERE state IN ('pending', 'running');

-- The running containers, so a restarted server knows what it started.
CREATE TABLE sandboxes (
    id          text PRIMARY KEY,
    kind        text        NOT NULL,
    project     uuid,
    container   text        NOT NULL,
    size        text,
    network     text,
    image       text        NOT NULL,
    started_at  timestamptz NOT NULL DEFAULT now(),
    removed_at  timestamptz
);

-- Every run belongs to a subtask. A run with no ticket has a subtask whose scope says what it
-- belongs to (15.3). The tickets table comes with M4.
CREATE TABLE subtasks (
    id          bigserial PRIMARY KEY,
    scope       text        NOT NULL CHECK (scope IN ('ticket', 'project', 'crew', 'conversation')),
    project     uuid,
    ticket      bigint,
    number      integer     NOT NULL,
    kind        text        NOT NULL,
    role        text        NOT NULL,
    state       text        NOT NULL DEFAULT 'queued',
    after       bigint[]    NOT NULL DEFAULT '{}',
    touches     text[]      NOT NULL DEFAULT '{}',
    parent      bigint REFERENCES subtasks (id),
    branch      text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One thread of turns in Crew (12.10), with the id each CLI gave it.
CREATE TABLE conversations (
    id          uuid PRIMARY KEY,
    project     uuid,
    ticket      bigint,
    purpose     text        NOT NULL,
    cli         text,
    cli_id      text,
    summary     text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    last_turn_at timestamptz
);

CREATE TABLE runs (
    id              uuid PRIMARY KEY,
    subtask         bigint      NOT NULL REFERENCES subtasks (id),
    number          integer     NOT NULL,
    state           text        NOT NULL DEFAULT 'starting',
    stop_reason     text,
    cli             text        NOT NULL,
    model           text,
    effort          text,
    model_name      text,
    model_resolved  text,
    model_from      text,
    effort_from     text,
    cli_version     text,
    crew_version    text        NOT NULL,
    image           text        NOT NULL,
    sandbox         text REFERENCES sandboxes (id),
    conversation    uuid REFERENCES conversations (id),
    started_at      timestamptz NOT NULL DEFAULT now(),
    ended_at        timestamptz,
    tokens_in       bigint      NOT NULL DEFAULT 0,
    tokens_out      bigint      NOT NULL DEFAULT 0,
    cost_usd        numeric(12, 4),
    prompt_sha256   text,
    UNIQUE (subtask, number)
);

-- The full prompt of every run, compressed (8.6).
CREATE TABLE run_prompts (
    run         uuid PRIMARY KEY REFERENCES runs (id),
    gzip        bytea       NOT NULL,
    parts       jsonb       NOT NULL DEFAULT '[]'::jsonb
);

-- A run's token, as its hash only. The token itself is never stored, and it ends with its run.
CREATE TABLE run_tokens (
    sha256      text PRIMARY KEY,
    run         uuid        NOT NULL REFERENCES runs (id),
    role        text        NOT NULL,
    project     uuid,
    ends_at     timestamptz NOT NULL
);

CREATE TABLE turns (
    id              bigserial PRIMARY KEY,
    conversation    uuid        NOT NULL REFERENCES conversations (id),
    number          integer     NOT NULL,
    run             uuid REFERENCES runs (id),
    speaker         text        NOT NULL,
    text            text        NOT NULL DEFAULT '',
    tool_calls      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    tokens          bigint      NOT NULL DEFAULT 0,
    rebuilt         boolean     NOT NULL DEFAULT false,
    at              timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation, number)
);

-- Every event of every run, split by month on the time (8.3). The partitions are made ahead by a
-- job in the server; a month that has ended changes only when a project or a conversation is deleted.
CREATE TABLE events (
    run             uuid,
    conversation    uuid,
    seq             bigint      NOT NULL,
    at              timestamptz NOT NULL,
    type            text        NOT NULL,
    payload         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    raw             text,
    CHECK (run IS NOT NULL OR conversation IS NOT NULL)
) PARTITION BY RANGE (at);
CREATE INDEX events_by_run ON events (run, seq);
CREATE INDEX events_by_conversation ON events (conversation, seq);

-- The CLI's own files of a conversation, as it wrote them in its home (12.10).
CREATE TABLE cli_files (
    conversation    uuid        NOT NULL REFERENCES conversations (id),
    path            text        NOT NULL,
    gzip            bytea       NOT NULL,
    sha256          text        NOT NULL,
    saved_at        timestamptz NOT NULL DEFAULT now(),
    keep_until      timestamptz NOT NULL,
    PRIMARY KEY (conversation, path)
);

-- The models each CLI offers (12.3).
CREATE TABLE models (
    cli         text        NOT NULL,
    name        text        NOT NULL,
    family      text,
    efforts     text[]      NOT NULL DEFAULT '{}',
    limit_group text,
    offered     boolean     NOT NULL DEFAULT true,
    added_by    text        NOT NULL DEFAULT 'cli',
    read_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cli, name)
);

-- The privileges that differ from the defaults every table gets (deploy/db/10-roles.sh). A dump
-- does not carry such a difference: a table it makes again gets the defaults. So migrate and restore
-- both end with this function, and a later step that adds such a rule adds it here.
CREATE FUNCTION crew_apply_grants() RETURNS void LANGUAGE sql SET search_path = public, pg_temp AS $$
    -- History is only added to (6.4).
    REVOKE UPDATE, DELETE, TRUNCATE ON changes FROM crew;
$$;
SELECT crew_apply_grants();

-- The server makes the month partitions of events ahead, as the role crew, which may not change the
-- schema. This function, owned by the role that may, makes one month's partition and nothing else.
CREATE FUNCTION crew_ensure_events_month(month date) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    first date := date_trunc('month', month)::date;
    name  text := format('events_%s', to_char(first, 'YYYY_MM'));
BEGIN
    IF to_regclass(name) IS NULL THEN
        EXECUTE format('CREATE TABLE %I PARTITION OF events FOR VALUES FROM (%L) TO (%L)',
                       name, first, (first + interval '1 month')::date);
    END IF;
    RETURN name;
END $$;
REVOKE ALL ON FUNCTION crew_ensure_events_month(date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION crew_ensure_events_month(date) TO crew;

SELECT crew_ensure_events_month(now()::date);
SELECT crew_ensure_events_month((now() + interval '1 month')::date);
