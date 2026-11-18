-- Design 0.95. A check run says whether the project's setup failed in its sandbox with a mark of its
-- own, which the pipeline counts by (13.9). Its detail's beginning said it before, and an errored
-- command's detail begins with the command, which an agent wrote. The runs stored before this step are
-- marked from their results: a command's error there is the harness's own text, never the command's
-- name, as the pipeline decides it since this step.
-- older versions read: yes
--
-- An older version does not read the column, and counts by the detail's beginning as before. A run it
-- ends after a roll back is stored unmarked, and this step does not run again: after the next update
-- such a run's setup error counts as one of Crew's three tries, which can make its ticket wait.
ALTER TABLE check_runs ADD COLUMN setup_failed boolean NOT NULL DEFAULT false;
-- The CASE reads the results only where they are an array: an AND gives no order of evaluation.
UPDATE check_runs c SET setup_failed = true
    WHERE CASE WHEN jsonb_typeof(c.results) = 'array'
        THEN EXISTS (SELECT 1 FROM jsonb_array_elements(c.results) r WHERE r->>'error' LIKE 'the project''s setup%')
        ELSE false END;
