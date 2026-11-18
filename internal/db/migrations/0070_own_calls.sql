-- Design 0.65 (M17, P2). The calls the operator's own tools made to the Crew MCP are kept 180 days
-- (6.5).
-- older versions read: yes
--
-- The role crew only adds to mcp_calls (schema step 7). The calls the operator's own tools made, with
-- a personal token and no run, go after 180 days through this function, which takes no time from its caller: the
-- server cannot remove a younger call, or a run's, which is part of its trajectory and kept for good.
CREATE FUNCTION crew_prune_own_calls() RETURNS bigint
LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    WITH gone AS (DELETE FROM mcp_calls WHERE run IS NULL AND token IS NOT NULL AND at < now() - interval '180 days' RETURNING 1)
    SELECT count(*) FROM gone;
$$;
REVOKE ALL ON FUNCTION crew_prune_own_calls() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION crew_prune_own_calls() TO crew;
