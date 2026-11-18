-- Design 0.49 (M15). Evals, overrides, improvements, the analytics cache, and the retrospectives
-- (9.6 to 9.8, 10.3 to 10.7, 15.3).
-- older versions read: yes
--
-- Every row of a project's evals and overrides carries its project, and is deleted with it (10.6). An
-- improvement is Crew's and stays when a project goes: its numbers per project go, the totals stay.

-- The runs of an eval have no ticket (9.6), and the improver's runs neither a project nor a draft
-- (10.4).
ALTER TABLE subtasks DROP CONSTRAINT subtasks_scope_check;
ALTER TABLE subtasks ADD CONSTRAINT subtasks_scope_check
    CHECK (scope IN ('ticket', 'project', 'crew', 'conversation', 'eval', 'improver'));

-- A known piece of work, replayed from the code as it was (9.6). number is the case's number in its
-- project, as "0003". origin is the Done ticket it was chosen from, the fixed bug, or the operator.
CREATE TABLE eval_cases (
    id            bigserial PRIMARY KEY,
    project       uuid        NOT NULL REFERENCES projects (id),
    number        integer     NOT NULL,
    title         text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    origin        text        NOT NULL CHECK (origin IN ('ticket', 'bug', 'operator')),
    origin_ticket bigint REFERENCES tickets (id),
    origin_bug    bigint REFERENCES bugs (id),
    -- The first parent of the ticket's merge commit: the code before the task was done.
    base_commit   text        NOT NULL,
    why           text        NOT NULL,
    what          text        NOT NULL,
    goal          text        NOT NULL,
    -- The ticket's command checks, as [{"cmd": "..."}].
    checks        jsonb       NOT NULL DEFAULT '[]'::jsonb,
    -- Copied in when the checks run, hidden from the agents.
    held_out      text[]      NOT NULL DEFAULT '{}',
    chosen_why    text        NOT NULL DEFAULT '',
    -- The role or operator that proposed it, and its run.
    proposed_by   text        NOT NULL,
    proposed_run  uuid REFERENCES runs (id),
    status        text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    retired_why   text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project, number)
);

-- One run of a project's suite: every active case, repeats times, on one side or both (10.5). purpose
-- says what asked for it; a change is an override of this project or an improvement of the baseline.
CREATE TABLE eval_suites (
    id           bigserial PRIMARY KEY,
    project      uuid        NOT NULL REFERENCES projects (id),
    purpose      text        NOT NULL CHECK (purpose IN ('on_demand', 'override', 'improvement')),
    override     bigint,
    improvement  bigint,
    repeats      integer     NOT NULL CHECK (repeats BETWEEN 1 AND 20),
    budget_usd   numeric     NOT NULL,
    cost_usd     numeric     NOT NULL DEFAULT 0,
    state        text        NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'done', 'stopped', 'failed')),
    stopped_why  text,
    -- The numbers that come back: per side, cases, passed shares per repeat, cost (10.5).
    result       jsonb,
    asked_by     text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    ended_at     timestamptz
);
CREATE INDEX eval_suites_by_project ON eval_suites (project, created_at);

-- One case, one repeat, one side: a builder, a reviewer, and a tester on eval/<run> (9.6), with what
-- it measured and what it ran on.
CREATE TABLE eval_runs (
    id            bigserial PRIMARY KEY,
    suite         bigint      NOT NULL REFERENCES eval_suites (id),
    project       uuid        NOT NULL REFERENCES projects (id),
    eval_case     bigint      NOT NULL REFERENCES eval_cases (id),
    repeat        integer     NOT NULL,
    side          text        NOT NULL CHECK (side IN ('baseline', 'change')),
    branch        text,
    state         text        NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'building', 'reviewing', 'testing', 'checking', 'fixing', 'done', 'failed', 'stopped')),
    goal_met      boolean,
    check_rounds  integer     NOT NULL DEFAULT 0,
    rework        integer     NOT NULL DEFAULT 0,
    cost_usd      numeric     NOT NULL DEFAULT 0,
    duration_s    integer,
    crew_version  text        NOT NULL,
    cli           text,
    cli_version   text,
    model         text,
    effort        text,
    -- The project's overrides that applied, as [{"path", "override", "version"}], and the patched paths.
    overrides     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    builder_run   uuid REFERENCES runs (id),
    reviewer_run  uuid REFERENCES runs (id),
    tester_run    uuid REFERENCES runs (id),
    detail        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    started_at    timestamptz,
    ended_at      timestamptz,
    UNIQUE (suite, eval_case, repeat, side)
);

-- A project's own version of one file of the baseline (10.3, 9.6): the whole file, never a patch.
-- One row per version; at most one is applied for a path.
CREATE TABLE overrides (
    id            bigserial PRIMARY KEY,
    project       uuid        NOT NULL REFERENCES projects (id),
    number        integer     NOT NULL,
    path          text        NOT NULL,
    version       integer     NOT NULL,
    text          text        NOT NULL CHECK (length(text) BETWEEN 1 AND 100000),
    why           text        NOT NULL DEFAULT '',
    -- waiting: for the operator (a checking role's file, or the prompt writer's); testing: for its
    -- evals, or for more cases; taken_back: by Crew, on a worse result; replaced: by a later version.
    status        text        NOT NULL DEFAULT 'proposed'
        CHECK (status IN ('proposed', 'waiting', 'testing', 'applied', 'refused', 'taken_back', 'withdrawn', 'replaced')),
    proposed_by   text        NOT NULL,
    proposed_run  uuid REFERENCES runs (id),
    suite         bigint REFERENCES eval_suites (id),
    eval          jsonb,
    decided_by    text,
    decided_why   text,
    applied_at    timestamptz,
    -- The Crew version and the project's ticket count when it was applied, and what it measured.
    applied_on    text,
    measure       jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz,
    UNIQUE (project, number),
    UNIQUE (project, path, version)
);
CREATE UNIQUE INDEX overrides_one_applied ON overrides (project, path) WHERE status = 'applied';
ALTER TABLE eval_suites ADD FOREIGN KEY (override) REFERENCES overrides (id);

-- A proposed change to the baseline and what became of it (10.5). patch holds whole new texts of
-- files, {path: text}; evidence the signals, tickets and projects behind it, which never enter a
-- project. waits_for says who an improvement in passed or proposed waits for.
CREATE TABLE improvements (
    id            bigserial PRIMARY KEY,
    number        integer     NOT NULL UNIQUE,
    title         text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    status        text        NOT NULL DEFAULT 'proposed'
        CHECK (status IN ('proposed', 'refused', 'testing', 'passed', 'applied', 'removed', 'rolled_back')),
    waits_for     text CHECK (waits_for IN ('operator', 'crew_project', 'cases')),
    target        text[]      NOT NULL,
    patch         jsonb       NOT NULL,
    evidence      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    body          text        NOT NULL DEFAULT '',
    expected      text        NOT NULL DEFAULT '',
    measure       text        NOT NULL,
    refused_why   text,
    evals         jsonb,
    decided_by    text,
    -- The ticket on Crew's own project (16.2).
    ticket        bigint REFERENCES tickets (id),
    release_from  text,
    release_to    text,
    result        jsonb,
    proposed_run  uuid REFERENCES runs (id),
    -- For a change of the prompt writer's rules, three earlier prompts rewritten by the old rules and
    -- by the new (7.2): [{"rewrite", "original", "old", "new"}].
    examples      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    decided_at    timestamptz,
    applied_at    timestamptz,
    measured_at   timestamptz,
    ended_at      timestamptz
);
ALTER TABLE eval_suites ADD FOREIGN KEY (improvement) REFERENCES improvements (id);

-- The numbers each project's evals gave an improvement: only numbers come back (10.5, step 3).
CREATE TABLE improvement_evals (
    improvement   bigint      NOT NULL REFERENCES improvements (id),
    project       uuid        NOT NULL REFERENCES projects (id),
    suite         bigint      NOT NULL REFERENCES eval_suites (id),
    cases         integer     NOT NULL,
    result        jsonb       NOT NULL,
    at            timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (improvement, project)
);

-- Analytics as computed, kept for a while and computed again when gone (9.7, 10.7).
CREATE TABLE metrics (
    key           text PRIMARY KEY,
    project       uuid REFERENCES projects (id),
    value         jsonb       NOT NULL,
    computed_at   timestamptz NOT NULL DEFAULT now()
);

-- One retrospective for each part of the plan that is complete, and one each time the project is Done
-- (part null) (9.8).
CREATE TABLE retrospectives (
    id            bigserial PRIMARY KEY,
    project       uuid        NOT NULL REFERENCES projects (id),
    part          text,
    run           uuid REFERENCES runs (id),
    tag           text,
    state         text        NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'running', 'done', 'failed')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz
);
CREATE UNIQUE INDEX retrospectives_once ON retrospectives (project, part) WHERE part IS NOT NULL;

-- When the operator last asked for the improver now (10.4, "Run the improver now").
ALTER TABLE crew_state ADD COLUMN improver_asked timestamptz;

-- The setting improvements.improver_network is gone (since 0.49): the improver is always Model only.
UPDATE settings SET doc = doc #- '{improvements,improver_network}' WHERE project IS NULL AND doc #> '{improvements,improver_network}' IS NOT NULL;

-- The function that deletes a project removes these too. Improvements stay; their ticket on a
-- deleted project is forgotten.
CREATE OR REPLACE FUNCTION crew_delete_project(p uuid, who text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    pname text; pkey text; pdraft uuid; pstate text; nt int; nr int;
BEGIN
    IF p = 'f1c50000-0000-4000-8000-000000000001' THEN
        RAISE EXCEPTION 'the fixture project is Crew''s own, and is not deleted';
    END IF;
    SELECT name, key, draft, state INTO pname, pkey, pdraft, pstate FROM projects WHERE id = p FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no project %', p;
    END IF;
    IF pstate <> 'archived' THEN
        RAISE EXCEPTION 'project % is %: it is archived first, so nothing starts in it', pname, pstate;
    END IF;
    IF EXISTS (SELECT 1 FROM runs r JOIN subtasks s ON s.id = r.subtask
            WHERE (s.project = p OR s.ticket IN (SELECT id FROM tickets WHERE project = p)) AND r.state <> 'ended') THEN
        RAISE EXCEPTION 'project % has runs that have not ended', pname;
    END IF;
    CREATE TEMP TABLE pg_temp.del_t ON COMMIT DROP AS SELECT id FROM tickets WHERE project = p;
    CREATE TEMP TABLE pg_temp.del_d ON COMMIT DROP AS SELECT id FROM plan_drafts WHERE project = p OR id = pdraft;
    CREATE TEMP TABLE pg_temp.del_s ON COMMIT DROP AS SELECT id FROM subtasks WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t)
        OR draft IN (SELECT id FROM pg_temp.del_d);
    CREATE TEMP TABLE pg_temp.del_r ON COMMIT DROP AS SELECT id FROM runs WHERE subtask IN (SELECT id FROM pg_temp.del_s);
    CREATE TEMP TABLE pg_temp.del_c ON COMMIT DROP AS SELECT id FROM conversations WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t)
        OR draft IN (SELECT id FROM pg_temp.del_d) OR id IN (SELECT conversation FROM runs WHERE id IN (SELECT id FROM pg_temp.del_r));
    CREATE TEMP TABLE pg_temp.del_b ON COMMIT DROP AS SELECT id FROM sandboxes WHERE project = p
        OR id IN (SELECT sandbox FROM runs WHERE id IN (SELECT id FROM pg_temp.del_r));
    SELECT count(*) INTO nt FROM pg_temp.del_t;
    SELECT count(*) INTO nr FROM pg_temp.del_r;

    UPDATE improvements SET ticket = NULL WHERE ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM improvement_evals WHERE project = p;
    DELETE FROM eval_runs WHERE project = p;
    UPDATE overrides SET suite = NULL WHERE project = p;
    DELETE FROM eval_suites WHERE project = p;
    DELETE FROM overrides WHERE project = p;
    DELETE FROM eval_cases WHERE project = p;
    DELETE FROM metrics WHERE project = p;
    DELETE FROM retrospectives WHERE project = p;
    DELETE FROM check_results WHERE ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM memory WHERE project = p;
    DELETE FROM bugs WHERE project = p;
    DELETE FROM feedback WHERE project = p;
    DELETE FROM skill_requests WHERE project = p;
    DELETE FROM doc_page_tickets WHERE project = p;
    DELETE FROM doc_pages WHERE project = p;
    DELETE FROM stage_moves WHERE ticket IN (SELECT id FROM pg_temp.del_t) OR run IN (SELECT id FROM pg_temp.del_r) OR subtask IN (SELECT id FROM pg_temp.del_s);
    DELETE FROM records WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t) OR run IN (SELECT id FROM pg_temp.del_r)
        OR delivered_run IN (SELECT id FROM pg_temp.del_r) OR subtask IN (SELECT id FROM pg_temp.del_s);
    DELETE FROM summaries WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t) OR subtask IN (SELECT id FROM pg_temp.del_s);
    DELETE FROM decisions WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t) OR change_ticket IN (SELECT id FROM pg_temp.del_t)
        OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM signals WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t) OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM mcp_calls WHERE project = p OR run IN (SELECT id FROM pg_temp.del_r) OR draft IN (SELECT id FROM pg_temp.del_d);
    DELETE FROM ref_pushes WHERE run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM run_prompts WHERE run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM run_spools WHERE run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM run_tokens WHERE project = p OR run IN (SELECT id FROM pg_temp.del_r) OR draft IN (SELECT id FROM pg_temp.del_d);
    DELETE FROM terminal_log WHERE project = p OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM turns WHERE conversation IN (SELECT id FROM pg_temp.del_c) OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM reports WHERE project = p OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM draft_messages WHERE draft IN (SELECT id FROM pg_temp.del_d) OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM plan_draft_versions WHERE draft IN (SELECT id FROM pg_temp.del_d) OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM prompt_rewrites WHERE project = p OR draft IN (SELECT id FROM pg_temp.del_d) OR run IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM events WHERE run IN (SELECT id FROM pg_temp.del_r) OR conversation IN (SELECT id FROM pg_temp.del_c);
    DELETE FROM cli_files WHERE conversation IN (SELECT id FROM pg_temp.del_c);
    DELETE FROM github_posts WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM pull_requests WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM pushes WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM remote_mains WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM checks WHERE ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM check_runs WHERE project = p OR ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM ticket_links WHERE from_ticket IN (SELECT id FROM pg_temp.del_t) OR to_ticket IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM subtask_steps WHERE subtask IN (SELECT id FROM pg_temp.del_s);
    DELETE FROM runs WHERE id IN (SELECT id FROM pg_temp.del_r);
    DELETE FROM subtasks WHERE id IN (SELECT id FROM pg_temp.del_s);
    DELETE FROM conversations WHERE id IN (SELECT id FROM pg_temp.del_c);
    DELETE FROM project_terminals WHERE project = p OR sandbox IN (SELECT id FROM pg_temp.del_b);
    DELETE FROM terminal_mains WHERE project = p;
    DELETE FROM sandbox_tokens WHERE sandbox IN (SELECT id FROM pg_temp.del_b);
    DELETE FROM sandboxes WHERE id IN (SELECT id FROM pg_temp.del_b);
    DELETE FROM tickets WHERE id IN (SELECT id FROM pg_temp.del_t);
    DELETE FROM plans WHERE project = p;
    DELETE FROM settings WHERE project = p;
    DELETE FROM git_repos WHERE project = p;
    DELETE FROM lead_waits WHERE project = p;
    DELETE FROM changes WHERE project = p;
    UPDATE projects SET draft = NULL WHERE id = p;
    DELETE FROM plan_drafts WHERE id IN (SELECT id FROM pg_temp.del_d);
    DELETE FROM projects WHERE id = p;
    INSERT INTO deleted_projects (name, key, deleted_by, tickets, runs) VALUES (pname, pkey, who, nt, nr);
END $$;
