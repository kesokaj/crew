-- M5: plan to OK (design 7.2, 11.5, 15.2, 15.3).

-- A plan draft (7.2, step 1): developed with the planning agent before any project exists. It may
-- name an existing GitHub repository the planning agent reads with the read-only token (12.6). It
-- becomes a project once, and then belongs to it.
CREATE TABLE plan_drafts (
    id          uuid PRIMARY KEY,
    title       text        NOT NULL DEFAULT '',
    state       text        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'project', 'closed')),
    repo        text CHECK (repo ~ '^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$'),
    project     uuid REFERENCES projects (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((state = 'project') = (project IS NOT NULL))
);

-- Every version of a draft's plan, as the planning agent wrote it with write_plan_draft. A row is
-- never changed: a new version is a new row.
CREATE TABLE plan_draft_versions (
    draft       uuid        NOT NULL REFERENCES plan_drafts (id),
    number      integer     NOT NULL,
    text        text        NOT NULL,
    run         uuid REFERENCES runs (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (draft, number)
);

-- The operator's messages in a draft. A message is given to the planning agent once: run says which
-- run took it, and a message no run took yet is what the orchestrator queues the planning agent for.
CREATE TABLE draft_messages (
    id          bigserial PRIMARY KEY,
    draft       uuid        NOT NULL REFERENCES plan_drafts (id),
    text        text        NOT NULL,
    run         uuid REFERENCES runs (id),
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX draft_messages_waiting ON draft_messages (draft, id) WHERE run IS NULL;

-- A run with no project may belong to a draft: its subtask, its conversation, and its token say so.
-- A draft's runs are counted at the Crew level and move to the project with the draft (7.2).
ALTER TABLE subtasks      ADD COLUMN draft uuid REFERENCES plan_drafts (id);
ALTER TABLE conversations ADD COLUMN draft uuid REFERENCES plan_drafts (id);
ALTER TABLE run_tokens    ADD COLUMN draft uuid REFERENCES plan_drafts (id);
CREATE INDEX subtasks_by_draft ON subtasks (draft) WHERE draft IS NOT NULL;

-- A project made from a draft names it, and a project with a remote names it: the address git
-- pushes to over SSH (6.6).
ALTER TABLE projects
    ADD COLUMN draft  uuid REFERENCES plan_drafts (id),
    ADD COLUMN remote text;

-- Every call to the Crew MCP (11.5): the run, the tool, the arguments without large bodies, the
-- result, and how long it took. Only added to.
CREATE TABLE mcp_calls (
    id          bigserial PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    run         uuid REFERENCES runs (id),
    project     uuid REFERENCES projects (id),
    draft       uuid REFERENCES plan_drafts (id),
    role        text        NOT NULL,
    tool        text        NOT NULL,
    args        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    ok          boolean     NOT NULL,
    result      text        NOT NULL DEFAULT '',
    ms          integer     NOT NULL DEFAULT 0
);
CREATE INDEX mcp_calls_by_project ON mcp_calls (project, id) WHERE project IS NOT NULL;
CREATE INDEX mcp_calls_by_run ON mcp_calls (run, id);

-- Every push to a project's remote (15.3): who asked, what, when, and the result. A push that
-- failed is pending again, and the board tries it again (21).
CREATE TABLE pushes (
    id          bigserial PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    remote      text        NOT NULL,
    refs        text[]      NOT NULL,
    who         text        NOT NULL,
    state       text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done')),
    tries       integer     NOT NULL DEFAULT 0,
    detail      text        NOT NULL DEFAULT '',
    not_before  timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    done_at     timestamptz
);
CREATE INDEX pushes_pending ON pushes (not_before) WHERE state = 'pending';

-- The versions of a draft and the calls to the MCP are history: the server may only add them.
CREATE OR REPLACE FUNCTION crew_apply_grants() RETURNS void LANGUAGE sql SET search_path = public, pg_temp AS $$
    REVOKE UPDATE, DELETE, TRUNCATE ON changes FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plans, stage_moves, check_results FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plan_draft_versions, mcp_calls FROM crew;
$$;
SELECT crew_apply_grants();
