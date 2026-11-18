-- Design 0.43, M10: judge: checks run (7.1). A judge's answer about a branch's head is a check run of
-- its own purpose, and In progress leaves only when it is met. A project that stores its stages
-- block whole gets the new exit check in its In progress stage, after task_cmd_checks_pass, so its
-- judge checks are not passed over.
ALTER TABLE check_runs DROP CONSTRAINT check_runs_purpose_check;
ALTER TABLE check_runs ADD CONSTRAINT check_runs_purpose_check
    CHECK (purpose IN ('base', 'exit', 'test', 'merge', 'part', 'join', 'judge'));

UPDATE settings s SET doc = jsonb_set(s.doc, '{stages}', (
        SELECT jsonb_agg(CASE
            WHEN st->>'id' = 'in_progress' AND jsonb_typeof(st->'exit') = 'array' AND NOT (st->'exit' ? 'task_judge_checks_pass')
            THEN jsonb_set(st, '{exit}', (
                SELECT jsonb_agg(e ORDER BY n) FROM (
                    SELECT e, n FROM jsonb_array_elements(st->'exit') WITH ORDINALITY AS x(e, n)
                    UNION ALL
                    SELECT '"task_judge_checks_pass"'::jsonb,
                        coalesce((SELECT n FROM jsonb_array_elements_text(st->'exit') WITH ORDINALITY AS y(v, n) WHERE v = 'task_cmd_checks_pass'),
                                 jsonb_array_length(st->'exit')) + 0.5
                ) z))
            ELSE st END ORDER BY i)
        FROM jsonb_array_elements(s.doc->'stages') WITH ORDINALITY AS a(st, i)))
    WHERE s.project IS NOT NULL AND jsonb_typeof(s.doc->'stages') = 'array' AND jsonb_array_length(s.doc->'stages') > 0;
