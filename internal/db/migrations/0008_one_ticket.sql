-- M6: one autonomous ticket (design 6.3, 7.1, 7.10, 7.11, 8.4, 10.2, 11.3, 11.5, 11.6, 13.9).

-- What an agent or the operator writes on a ticket (6.3): notes, reviews, test entries, and comments.
-- A comment with no ticket is a comment on the project's plan (11.6). A review and a test entry name
-- the commit of the ticket's branch they are about, so a later fix makes them old.
CREATE TABLE records (
    id            bigserial PRIMARY KEY,
    project       uuid        NOT NULL REFERENCES projects (id),
    ticket        bigint REFERENCES tickets (id),
    kind          text        NOT NULL CHECK (kind IN ('note', 'review', 'test', 'comment')),
    -- operator, or an agent's role
    author        text        NOT NULL,
    run           uuid REFERENCES runs (id),
    subtask       bigint REFERENCES subtasks (id),
    commit        text,
    verdict       text CHECK (verdict IN ('approve', 'changes_requested')),
    result        text CHECK (result IN ('pass', 'fail')),
    body          text        NOT NULL,
    in_reply_to   bigint REFERENCES records (id),
    resolved      boolean     NOT NULL DEFAULT false,
    -- a comment for the agent: the run it was given to, or null while it waits for the next run
    delivered_run uuid REFERENCES runs (id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (ticket IS NOT NULL OR kind = 'comment'),
    CHECK ((kind = 'review') = (verdict IS NOT NULL)),
    CHECK ((kind = 'test') = (result IS NOT NULL))
);
CREATE INDEX records_by_ticket ON records (ticket, id);
CREATE INDEX records_by_project ON records (project, id);

-- A choice an agent made that the plan does not settle (7.5, decide). The run carries on; the
-- operator can change it later (M7).
CREATE TABLE decisions (
    id            bigserial PRIMARY KEY,
    project       uuid        NOT NULL REFERENCES projects (id),
    ticket        bigint REFERENCES tickets (id),
    run           uuid REFERENCES runs (id),
    role          text        NOT NULL,
    question      text        NOT NULL,
    alternatives  jsonb       NOT NULL DEFAULT '[]'::jsonb,
    choice        text        NOT NULL,
    reason        text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX decisions_by_project ON decisions (project, id);

-- The checklist a run writes for its own subtask and ticks off (7.11, set_steps and tick_step).
CREATE TABLE subtask_steps (
    subtask       bigint      NOT NULL REFERENCES subtasks (id),
    position      integer     NOT NULL,
    text          text        NOT NULL,
    done_at       timestamptz,
    PRIMARY KEY (subtask, position)
);

-- A subtask on a ticket: what its run reported with the call that ends its kind of run (11.3), the
-- goal-loop rounds of its run, whether it was reminded of a missing call, the commit its stage agent
-- judged, and when and how it ended.
ALTER TABLE subtasks
    ADD COLUMN reported    jsonb,
    ADD COLUMN goal_rounds integer     NOT NULL DEFAULT 0,
    ADD COLUMN reminded    boolean     NOT NULL DEFAULT false,
    ADD COLUMN commit      text,
    ADD COLUMN result      text,
    ADD COLUMN ended_at    timestamptz,
    -- a summary writer's subtask that summarises a conversation's turns through a number (12.10)
    ADD COLUMN summarises  uuid REFERENCES conversations (id),
    ADD COLUMN through     integer;
CREATE UNIQUE INDEX subtasks_number_on_ticket ON subtasks (ticket, number) WHERE ticket IS NOT NULL;

-- One run of a check sandbox (13.9): the ticket's command checks on its base before it is built,
-- the exit checks of a stage on the branch's head, the checks of Test, or the merge candidate.
-- results holds each command with its exit code, whether it passed, and the end of its output.
CREATE TABLE check_runs (
    id            bigserial PRIMARY KEY,
    project       uuid        NOT NULL REFERENCES projects (id),
    ticket        bigint      NOT NULL REFERENCES tickets (id),
    purpose       text        NOT NULL CHECK (purpose IN ('base', 'exit', 'test', 'merge')),
    commit        text        NOT NULL,
    -- for a merge: main as it was when the candidate was made, and the ticket's head merged into it
    main          text,
    head          text,
    state         text        NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'passed', 'failed', 'error')),
    results       jsonb       NOT NULL DEFAULT '[]'::jsonb,
    detail        text,
    flaky         boolean     NOT NULL DEFAULT false,
    started_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz
);
CREATE INDEX check_runs_by_ticket ON check_runs (ticket, id);

-- A command check may be one that must keep passing, which passes before the work too (7.6, M6):
-- the base check refuses every other command check that passes on the ticket's base.
ALTER TABLE checks ADD COLUMN keeps boolean NOT NULL DEFAULT false;

-- A run that judges or analyses reads its branch and pushes nothing (6.1).
ALTER TABLE runs ADD COLUMN push boolean NOT NULL DEFAULT true;

-- A finished ticket's summary (8.4): the facts from the records, and three sections a model wrote,
-- missing until a summary writer wrote them.
CREATE TABLE summaries (
    ticket        bigint PRIMARY KEY REFERENCES tickets (id),
    project       uuid        NOT NULL REFERENCES projects (id),
    outcome       text        NOT NULL CHECK (outcome IN ('done', 'discarded', 'cancelled')),
    facts         jsonb       NOT NULL,
    done          text,
    how           text,
    next          text,
    missing       boolean     NOT NULL DEFAULT true,
    subtask       bigint REFERENCES subtasks (id),
    tries         integer     NOT NULL DEFAULT 0,
    written_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    search        tsvector GENERATED ALWAYS AS (to_tsvector('simple',
                      coalesce(done, '') || ' ' || coalesce(how, '') || ' ' || coalesce(next, ''))) STORED
);
CREATE INDEX summaries_by_project ON summaries (project);
CREATE INDEX summaries_search ON summaries USING gin (search);

-- Crew's own summary of a conversation's older turns, for one rebuilt from its turns (12.10): the
-- number of the last turn it covers.
ALTER TABLE conversations ADD COLUMN summary_through integer NOT NULL DEFAULT 0;

-- A fact about how the agents and the pipeline did (10.2), recorded by the backend, never by an agent.
CREATE TABLE signals (
    id            bigserial PRIMARY KEY,
    project       uuid REFERENCES projects (id),
    ticket        bigint REFERENCES tickets (id),
    run           uuid REFERENCES runs (id),
    kind          text        NOT NULL,
    role          text,
    cli           text,
    model         text,
    effort        text,
    crew_version  text,
    detail        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX signals_by_project ON signals (project, kind, at);

CREATE OR REPLACE FUNCTION crew_apply_grants() RETURNS void LANGUAGE sql SET search_path = public, pg_temp AS $$
    REVOKE UPDATE, DELETE, TRUNCATE ON changes FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plans, stage_moves, check_results FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plan_draft_versions, mcp_calls FROM crew;
    -- A signal is a fact: only added to (10.2).
    REVOKE UPDATE, DELETE, TRUNCATE ON signals FROM crew;
$$;
SELECT crew_apply_grants();
