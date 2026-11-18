-- Design 0.44 (M11), from the second review round of M11: the row that says a project existed is
-- only added to, in the function every migration runs again, as the other rows that are only added.
CREATE OR REPLACE FUNCTION crew_apply_grants() RETURNS void LANGUAGE sql SET search_path = public, pg_temp AS $$
    REVOKE UPDATE, DELETE, TRUNCATE ON changes FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plans, stage_moves, check_results FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON plan_draft_versions, mcp_calls FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON signals FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON ref_pushes FROM crew;
    REVOKE UPDATE, DELETE, TRUNCATE ON deleted_projects FROM crew;
$$;
SELECT crew_apply_grants();
