-- M7: the web UI (design 6.7, 20.1, 20.2).

-- Every ref a push through the git endpoint changed, with the commit before and after it: which run,
-- or the operator's terminal, pushed which commits (6.7). A ticket's commits are grouped by the
-- subtask whose run pushed them, and a subtask shows the diff of only its run. Only added to.
CREATE TABLE ref_pushes (
    id          bigserial PRIMARY KEY,
    repo        text        NOT NULL,
    run         uuid REFERENCES runs (id),
    who         text        NOT NULL,
    ref         text        NOT NULL,
    old         text,
    new         text        NOT NULL,
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ref_pushes_by_ref ON ref_pushes (repo, ref, id);
CREATE INDEX ref_pushes_by_run ON ref_pushes (run) WHERE run IS NOT NULL;

CREATE OR REPLACE FUNCTION crew_apply_grants() RETURNS void LANGUAGE sql SET search_path = public, pg_temp AS $$
    REVOKE UPDATE, DELETE, TRUNCATE ON changes FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plans, stage_moves, check_results FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plan_draft_versions, mcp_calls FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON signals FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON ref_pushes FROM crew;
$$;
SELECT crew_apply_grants();
