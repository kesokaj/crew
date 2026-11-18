-- Design 0.44 (M11), from the security review of M11. The function that deletes a project checks for
-- itself what the server checks before it, so a caller that runs SQL as crew cannot delete Crew's
-- fixture, a project that is not archived, or one whose runs still go; its temporary tables are named
-- in pg_temp. The row that says a project existed can only be added to.
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

REVOKE UPDATE, DELETE, TRUNCATE ON deleted_projects FROM crew;
