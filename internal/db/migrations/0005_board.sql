-- M4: the board core (design 6.1 to 6.4, 7.1, 7.3, 7.4, 7.7, 7.12, 15.3).

-- A project: its rows here, its code in two repositories of the same name, the canonical one and the
-- quarantine one that sandboxes reach through the git endpoint (6.1). The name is the repositories'
-- name; the key is the prefix of its ticket numbers (7.12). Neither changes after it is created.
CREATE TABLE projects (
    id            uuid PRIMARY KEY,
    name          text        NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    key           text        NOT NULL UNIQUE CHECK (key ~ '^[A-Z][A-Z0-9]{0,9}$'),
    goal          text        NOT NULL DEFAULT '',
    state         text        NOT NULL DEFAULT 'planning'
                  CHECK (state IN ('planning', 'running', 'paused', 'done', 'archived')),
    pause_reason  text,
    -- how its repository was made: local, existing (with the address it was cloned from), or import
    repo_source   text        NOT NULL,
    next_number   integer     NOT NULL DEFAULT 1,
    ok_at         timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- The fixture project of M3 becomes a row, with the id its runs already carry. Its repository is the
-- git endpoint's fixture repository.
INSERT INTO projects (id, name, key, goal, state, repo_source)
VALUES ('f1c50000-0000-4000-8000-000000000001', 'fixture', 'FIX',
        'The fixture project of Crew''s own runs and tests.', 'running', 'local');

-- The plan of a project (7.2): the original, never changed; the breakdown; the amendments; and the
-- comments the operator sent the breakdown back with. A row is never changed: a new version is a
-- new row.
CREATE TABLE plans (
    id          bigserial PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    kind        text        NOT NULL CHECK (kind IN ('original', 'breakdown', 'amendment', 'sent_back')),
    number      integer     NOT NULL,
    text        text        NOT NULL,
    created_by  text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project, kind, number)
);
CREATE UNIQUE INDEX plans_one_original ON plans (project) WHERE kind = 'original';

-- A ticket (6.3, 7.1, 7.12). The stage is a stage id of the project's settings, or closed. The
-- analysis holds its status, who decided, the reason, the condition to look again, and may_change.
-- position orders the backlog within a priority: a ticket the operator adds goes to the top (7.9).
CREATE TABLE tickets (
    id            bigserial PRIMARY KEY,
    project       uuid        NOT NULL REFERENCES projects (id),
    number        integer     NOT NULL,
    title         text        NOT NULL,
    kind          text        NOT NULL CHECK (kind IN ('feature', 'fix', 'chore', 'docs')),
    part          text,
    source        jsonb,
    why           text        NOT NULL DEFAULT '',
    what          text        NOT NULL DEFAULT '',
    goal          text        NOT NULL DEFAULT '',
    approach      text,
    docs          text[]      NOT NULL DEFAULT '{}',
    docs_reason   text,
    stage         text        NOT NULL,
    closed_reason text        CHECK (closed_reason IN ('discarded', 'cancelled')),
    skips         text[]      NOT NULL DEFAULT '{}',
    skips_reason  text,
    origin        text        NOT NULL CHECK (origin IN ('plan', 'agent', 'operator', 'token', 'conversation', 'chat')),
    created_by    text        NOT NULL,
    seen          text,
    analysis      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    priority      smallint    NOT NULL DEFAULT 2 CHECK (priority BETWEEN 0 AND 3),
    position      bigint      NOT NULL,
    budget_usd    numeric(12, 2),
    hold          boolean     NOT NULL DEFAULT false,
    notice_until  timestamptz,
    agents        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    network       text        NOT NULL DEFAULT 'inherit',
    branch        text,
    pull_request  integer,
    fix_rounds    integer     NOT NULL DEFAULT 0,
    over_budget   timestamptz,
    -- why the ticket does not move now, as the board last saw it; not history
    waiting       text,
    stage_at      timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project, number),
    CHECK ((stage = 'closed') = (closed_reason IS NOT NULL))
);
CREATE INDEX tickets_by_stage ON tickets (project, stage);

-- What a ticket depends on, was created from, duplicates, fixes, or replaces (7.12). No circle of
-- depends_on: the board refuses a link that would close one (7.9).
CREATE TABLE ticket_links (
    from_ticket bigint NOT NULL REFERENCES tickets (id),
    to_ticket   bigint NOT NULL REFERENCES tickets (id),
    kind        text   NOT NULL CHECK (kind IN ('depends_on', 'created_from', 'duplicates', 'fixes_bug', 'replaces')),
    PRIMARY KEY (from_ticket, to_ticket, kind),
    CHECK (from_ticket <> to_ticket)
);
CREATE INDEX ticket_links_to ON ticket_links (to_ticket, kind);

-- A ticket's own checks (7.1): commands and judge criteria, set in Analysis. The backend reads them
-- here, never from the ticket's branch.
CREATE TABLE checks (
    ticket      bigint  NOT NULL REFERENCES tickets (id),
    position    integer NOT NULL,
    kind        text    NOT NULL CHECK (kind IN ('cmd', 'judge')),
    spec        text    NOT NULL,
    PRIMARY KEY (ticket, position)
);

-- Every move of a ticket (7.4): only to the right, or to closed.
CREATE TABLE stage_moves (
    id          bigserial PRIMARY KEY,
    ticket      bigint      NOT NULL REFERENCES tickets (id),
    from_stage  text        NOT NULL,
    to_stage    text        NOT NULL,
    actor       text        NOT NULL,
    reason      text,
    subtask     bigint REFERENCES subtasks (id),
    run         uuid REFERENCES runs (id),
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX stage_moves_by_ticket ON stage_moves (ticket, id);

-- The result of each check that let a move happen, saved with the move (6.4).
CREATE TABLE check_results (
    id          bigserial PRIMARY KEY,
    ticket      bigint      NOT NULL REFERENCES tickets (id),
    move        bigint REFERENCES stage_moves (id),
    stage       text        NOT NULL,
    name        text        NOT NULL,
    passed      boolean     NOT NULL,
    detail      text        NOT NULL DEFAULT '',
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX check_results_by_ticket ON check_results (ticket, id);

-- The history of a project's rows names the project, so its history can be shown, and removed with
-- it (6.4).
ALTER TABLE changes ADD COLUMN project uuid;
CREATE INDEX changes_by_project ON changes (project, seq) WHERE project IS NOT NULL;

-- Every row that belongs to a project carries it (15.3); now that projects are rows, it is a key.
ALTER TABLE settings      ADD FOREIGN KEY (project) REFERENCES projects (id);
ALTER TABLE sandboxes     ADD FOREIGN KEY (project) REFERENCES projects (id);
ALTER TABLE subtasks      ADD FOREIGN KEY (project) REFERENCES projects (id);
ALTER TABLE subtasks      ADD FOREIGN KEY (ticket) REFERENCES tickets (id);
ALTER TABLE conversations ADD FOREIGN KEY (project) REFERENCES projects (id);
ALTER TABLE conversations ADD FOREIGN KEY (ticket) REFERENCES tickets (id);
ALTER TABLE run_tokens    ADD FOREIGN KEY (project) REFERENCES projects (id);
ALTER TABLE git_repos     ADD FOREIGN KEY (project) REFERENCES projects (id);
INSERT INTO git_repos (name, project) VALUES ('fixture', 'f1c50000-0000-4000-8000-000000000001')
ON CONFLICT (name) DO UPDATE SET project = excluded.project;

-- A plan's row is never changed (7.2), and a move is history: the server may only add them.
CREATE OR REPLACE FUNCTION crew_apply_grants() RETURNS void LANGUAGE sql SET search_path = public, pg_temp AS $$
    -- History is only added to (6.4).
    REVOKE UPDATE, DELETE, TRUNCATE ON changes FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plans, stage_moves, check_results FROM crew;
$$;
SELECT crew_apply_grants();
