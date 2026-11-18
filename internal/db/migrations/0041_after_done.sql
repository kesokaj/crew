-- Design 0.44 (M11). After Done (7.13): a revert is a ticket of the kind revert that names the merge it
-- undoes and links to the Done ticket with reverts; an amendment's tickets name it, and its OK is a
-- row of its own, since a plan's rows are never changed; a deleted project leaves one row that says it
-- existed, with no reference to it.
ALTER TABLE tickets DROP CONSTRAINT tickets_kind_check;
ALTER TABLE tickets ADD CONSTRAINT tickets_kind_check CHECK (kind IN ('feature', 'fix', 'chore', 'docs', 'revert'));
ALTER TABLE tickets ADD COLUMN revert_of text;
ALTER TABLE tickets ADD COLUMN amendment bigint REFERENCES plans (id);
ALTER TABLE ticket_links DROP CONSTRAINT ticket_links_kind_check;
ALTER TABLE ticket_links ADD CONSTRAINT ticket_links_kind_check
    CHECK (kind IN ('depends_on', 'created_from', 'duplicates', 'fixes_bug', 'replaces', 'reverts'));
ALTER TABLE plans DROP CONSTRAINT plans_kind_check;
ALTER TABLE plans ADD CONSTRAINT plans_kind_check CHECK (kind IN ('original', 'breakdown', 'amendment', 'sent_back', 'amendment_ok'));

CREATE TABLE deleted_projects (
    id          bigserial   PRIMARY KEY,
    name        text        NOT NULL,
    key         text        NOT NULL,
    deleted_at  timestamptz NOT NULL DEFAULT now(),
    deleted_by  text        NOT NULL,
    tickets     int         NOT NULL,
    runs        int         NOT NULL
);

-- Deleting a project removes every row of it, its history too (7.13): the server's role may not delete
-- from the tables that are only added to, so this one function, owned by the role that may, does it
-- for one project and nothing else, in the caller's transaction.
CREATE FUNCTION crew_delete_project(p uuid, who text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    pname text; pkey text; pdraft uuid; nt int; nr int;
BEGIN
    SELECT name, key, draft INTO pname, pkey, pdraft FROM projects WHERE id = p FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no project %', p;
    END IF;
    CREATE TEMP TABLE del_t ON COMMIT DROP AS SELECT id FROM tickets WHERE project = p;
    CREATE TEMP TABLE del_d ON COMMIT DROP AS SELECT id FROM plan_drafts WHERE project = p OR id = pdraft;
    CREATE TEMP TABLE del_s ON COMMIT DROP AS SELECT id FROM subtasks WHERE project = p OR ticket IN (SELECT id FROM del_t)
        OR draft IN (SELECT id FROM del_d);
    CREATE TEMP TABLE del_r ON COMMIT DROP AS SELECT id FROM runs WHERE subtask IN (SELECT id FROM del_s);
    CREATE TEMP TABLE del_c ON COMMIT DROP AS SELECT id FROM conversations WHERE project = p OR ticket IN (SELECT id FROM del_t)
        OR draft IN (SELECT id FROM del_d) OR id IN (SELECT conversation FROM runs WHERE id IN (SELECT id FROM del_r));
    CREATE TEMP TABLE del_b ON COMMIT DROP AS SELECT id FROM sandboxes WHERE project = p
        OR id IN (SELECT sandbox FROM runs WHERE id IN (SELECT id FROM del_r));
    SELECT count(*) INTO nt FROM del_t;
    SELECT count(*) INTO nr FROM del_r;

    DELETE FROM check_results WHERE ticket IN (SELECT id FROM del_t);
    DELETE FROM stage_moves WHERE ticket IN (SELECT id FROM del_t) OR run IN (SELECT id FROM del_r) OR subtask IN (SELECT id FROM del_s);
    DELETE FROM records WHERE project = p OR ticket IN (SELECT id FROM del_t) OR run IN (SELECT id FROM del_r)
        OR delivered_run IN (SELECT id FROM del_r) OR subtask IN (SELECT id FROM del_s);
    DELETE FROM summaries WHERE project = p OR ticket IN (SELECT id FROM del_t) OR subtask IN (SELECT id FROM del_s);
    DELETE FROM decisions WHERE project = p OR ticket IN (SELECT id FROM del_t) OR change_ticket IN (SELECT id FROM del_t)
        OR run IN (SELECT id FROM del_r);
    DELETE FROM signals WHERE project = p OR ticket IN (SELECT id FROM del_t) OR run IN (SELECT id FROM del_r);
    DELETE FROM mcp_calls WHERE project = p OR run IN (SELECT id FROM del_r) OR draft IN (SELECT id FROM del_d);
    DELETE FROM ref_pushes WHERE run IN (SELECT id FROM del_r);
    DELETE FROM run_prompts WHERE run IN (SELECT id FROM del_r);
    DELETE FROM run_spools WHERE run IN (SELECT id FROM del_r);
    DELETE FROM run_tokens WHERE project = p OR run IN (SELECT id FROM del_r) OR draft IN (SELECT id FROM del_d);
    DELETE FROM terminal_log WHERE project = p OR run IN (SELECT id FROM del_r);
    DELETE FROM turns WHERE conversation IN (SELECT id FROM del_c) OR run IN (SELECT id FROM del_r);
    DELETE FROM reports WHERE project = p OR run IN (SELECT id FROM del_r);
    DELETE FROM draft_messages WHERE draft IN (SELECT id FROM del_d) OR run IN (SELECT id FROM del_r);
    DELETE FROM plan_draft_versions WHERE draft IN (SELECT id FROM del_d) OR run IN (SELECT id FROM del_r);
    DELETE FROM prompt_rewrites WHERE project = p OR draft IN (SELECT id FROM del_d) OR run IN (SELECT id FROM del_r);
    DELETE FROM events WHERE run IN (SELECT id FROM del_r) OR conversation IN (SELECT id FROM del_c);
    DELETE FROM cli_files WHERE conversation IN (SELECT id FROM del_c);
    DELETE FROM github_posts WHERE project = p OR ticket IN (SELECT id FROM del_t);
    DELETE FROM pull_requests WHERE project = p OR ticket IN (SELECT id FROM del_t);
    DELETE FROM pushes WHERE project = p OR ticket IN (SELECT id FROM del_t);
    DELETE FROM remote_mains WHERE project = p OR ticket IN (SELECT id FROM del_t);
    DELETE FROM checks WHERE ticket IN (SELECT id FROM del_t);
    DELETE FROM check_runs WHERE project = p OR ticket IN (SELECT id FROM del_t);
    DELETE FROM ticket_links WHERE from_ticket IN (SELECT id FROM del_t) OR to_ticket IN (SELECT id FROM del_t);
    DELETE FROM subtask_steps WHERE subtask IN (SELECT id FROM del_s);
    DELETE FROM runs WHERE id IN (SELECT id FROM del_r);
    DELETE FROM subtasks WHERE id IN (SELECT id FROM del_s);
    DELETE FROM conversations WHERE id IN (SELECT id FROM del_c);
    DELETE FROM project_terminals WHERE project = p OR sandbox IN (SELECT id FROM del_b);
    DELETE FROM terminal_mains WHERE project = p;
    DELETE FROM sandbox_tokens WHERE sandbox IN (SELECT id FROM del_b);
    DELETE FROM sandboxes WHERE id IN (SELECT id FROM del_b);
    DELETE FROM tickets WHERE id IN (SELECT id FROM del_t);
    DELETE FROM plans WHERE project = p;
    DELETE FROM settings WHERE project = p;
    DELETE FROM git_repos WHERE project = p;
    DELETE FROM lead_waits WHERE project = p;
    DELETE FROM changes WHERE project = p;
    UPDATE projects SET draft = NULL WHERE id = p;
    DELETE FROM plan_drafts WHERE id IN (SELECT id FROM del_d);
    DELETE FROM projects WHERE id = p;
    INSERT INTO deleted_projects (name, key, deleted_by, tickets, runs) VALUES (pname, pkey, who, nt, nr);
END $$;
REVOKE ALL ON FUNCTION crew_delete_project(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION crew_delete_project(uuid, text) TO crew;
