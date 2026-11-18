-- Design 0.55. Crew's project is never Done (16.2, 7.2).
-- older versions read: yes
--
-- The operator chose (report 10, answer 2) that Crew's project, which has no plan, is never Done: it is
-- running or paused, and its last ticket's Done runs only that ticket's own steps. On dave its first
-- ticket made it Done, which cannot be paused. The operator's plan for that ticket was to pause the
-- project again after it, so this pauses Crew's project, the one the machine's improvements.crew_project
-- names, where it is Done, and records the change as the operator's own pause: a pause the backend made
-- is resumed by itself once no limit is passed (HoldLimits), and Crew's project must not start so. The
-- record says the step made it (source). An older version reads a paused project, as before.
WITH crew AS (
    SELECT p.id, p.pause_reason FROM projects p
    WHERE p.state = 'done' AND NOT p.hidden
      AND p.name = (SELECT m.doc->'improvements'->>'crew_project' FROM settings m WHERE m.project IS NULL)
), paused AS (
    UPDATE projects p SET state = 'paused', pause_reason = 'paused after its ticket: Crew''s project is never Done', updated_at = now()
    FROM crew WHERE p.id = crew.id
    RETURNING p.id, p.pause_reason
)
INSERT INTO changes (project, who, tbl, row_key, before, after)
SELECT paused.id, 'operator', 'projects', paused.id::text, jsonb_build_object('state', 'done', 'pauseReason', crew.pause_reason),
    jsonb_build_object('state', 'paused', 'pauseReason', paused.pause_reason, 'source', 'schema step 65')
FROM paused JOIN crew ON crew.id = paused.id;
