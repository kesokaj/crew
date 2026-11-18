-- Design 0.46 (M12). A project's knowledge (9): memory, bugs, feedback, the index of its documentation
-- pages, and the requests for a skill (12.9). Every row carries its project, and no tool an agent calls
-- names another (9.2). Search is PostgreSQL's full-text search with the simple configuration (9.3).

-- A short hint for later runs (9.3). number is the entry's number in its project, as an export writes it.
CREATE TABLE memory (
    id              bigserial PRIMARY KEY,
    project         uuid        NOT NULL REFERENCES projects (id),
    number          integer     NOT NULL,
    kind            text        NOT NULL CHECK (kind IN ('convention', 'fact', 'decision', 'lesson')),
    title           text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body            text        NOT NULL DEFAULT '' CHECK (length(body) <= 4000),
    applies_to      text[]      NOT NULL DEFAULT '{all}',
    paths           text[]      NOT NULL DEFAULT '{}',
    keywords        text[]      NOT NULL DEFAULT '{}',
    source_run      uuid REFERENCES runs (id),
    source_ticket   bigint REFERENCES tickets (id),
    source_feedback bigint,
    -- an agent role, or operator; an entry the operator wrote is not changed by an agent (9.3)
    author          text        NOT NULL,
    status          text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    retired_reason  text,
    changed_by      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    search          tsvector GENERATED ALWAYS AS (to_tsvector('simple', title || ' ' || body)) STORED,
    UNIQUE (project, number)
);
CREATE INDEX memory_search ON memory USING gin (search);

-- An observed defect (9.4), with the fix ticket made for it. found_by is an agent role, operator, or
-- system for a result that keeps changing (21). closed_reason discarded is Analysis's discard of the
-- fix ticket, with its words in closed_note.
CREATE TABLE bugs (
    id               bigserial PRIMARY KEY,
    project          uuid        NOT NULL REFERENCES projects (id),
    number           integer     NOT NULL,
    title            text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body             text        NOT NULL DEFAULT '',
    severity         text        NOT NULL CHECK (severity IN ('low', 'medium', 'high')),
    status           text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_work', 'fixed', 'closed')),
    found_by         text        NOT NULL,
    found_ticket     bigint REFERENCES tickets (id),
    found_run        uuid REFERENCES runs (id),
    fix_ticket       bigint REFERENCES tickets (id),
    regression_check text,
    closed_reason    text CHECK (closed_reason IN ('duplicate', 'not_reproducible', 'wont_fix', 'discarded')),
    closed_note      text,
    -- the end-to-end command whose result keeps changing, and how often it did (21)
    flaky_cmd        text,
    repeats          integer     NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project, number),
    CHECK ((status = 'closed') = (closed_reason IS NOT NULL))
);
CREATE INDEX bugs_by_fix_ticket ON bugs (fix_ticket);

-- A judgment of someone's work that should change future work (9.5). from_role is operator, an agent
-- role, or system for what the backend captured.
CREATE TABLE feedback (
    id           bigserial PRIMARY KEY,
    project      uuid        NOT NULL REFERENCES projects (id),
    number       integer     NOT NULL,
    from_role    text        NOT NULL,
    from_run     uuid REFERENCES runs (id),
    about        text        NOT NULL,
    kind         text        NOT NULL CHECK (kind IN ('praise', 'correction', 'rule')),
    ticket       bigint REFERENCES tickets (id),
    record       text,
    body         text        NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    status       text        NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'applied', 'dismissed')),
    outcome      text,
    resolved_by  text,
    resolved_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project, number),
    CHECK (kind <> 'rule' OR from_role = 'operator'),
    CHECK (from_role <> about OR from_role IN ('operator', 'system'))
);
ALTER TABLE memory ADD CONSTRAINT memory_source_feedback_fkey FOREIGN KEY (source_feedback) REFERENCES feedback (id);

-- The documentation pages on main, as Crew indexed them (9.9). covers is the page's front matter; a
-- merge that changes covered code and not the page marks it with outdated_since.
CREATE TABLE doc_pages (
    project         uuid        NOT NULL REFERENCES projects (id),
    path            text        NOT NULL,
    title           text        NOT NULL,
    kind            text        NOT NULL,
    body            text        NOT NULL,
    covers          text[]      NOT NULL DEFAULT '{}',
    blob            text        NOT NULL,
    last_commit     text        NOT NULL,
    last_changed_at timestamptz NOT NULL,
    last_ticket     bigint REFERENCES tickets (id),
    outdated_since  timestamptz,
    outdated_by     text,
    indexed_at      timestamptz NOT NULL DEFAULT now(),
    search          tsvector GENERATED ALWAYS AS (to_tsvector('simple', title || ' ' || body)) STORED,
    PRIMARY KEY (project, path)
);
CREATE INDEX doc_pages_search ON doc_pages USING gin (search);

-- The commit of main a project's pages were last indexed at.
ALTER TABLE projects ADD COLUMN docs_indexed text;

-- Each ticket that changed a page, from its merge (9.9).
CREATE TABLE doc_page_tickets (
    project  uuid        NOT NULL REFERENCES projects (id),
    path     text        NOT NULL,
    ticket   bigint REFERENCES tickets (id),
    commit   text        NOT NULL,
    author   text        NOT NULL,
    at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project, path, commit)
);

-- A decision record (9.9, record_decision) is a decision with its context; its page is written into
-- the ticket's merge commit.
ALTER TABLE decisions ADD COLUMN record boolean NOT NULL DEFAULT false;
ALTER TABLE decisions ADD COLUMN context text;
ALTER TABLE decisions ADD COLUMN page text;

-- A ticket that finds or writes a skill (12.9), and the requests it carries.
ALTER TABLE tickets ADD COLUMN skill boolean NOT NULL DEFAULT false;
CREATE TABLE skill_requests (
    id          bigserial PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    ticket      bigint      NOT NULL REFERENCES tickets (id),
    run         uuid REFERENCES runs (id),
    role        text        NOT NULL,
    missing     text        NOT NULL,
    seen_in     text        NOT NULL,
    wanted      text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- The function that deletes a project removes its knowledge too, before its tickets and runs.
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
