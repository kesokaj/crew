-- Design 0.51 (M16, P1). An eval case's fit check, and the kept output of a failed held-out check
-- (9.6).
-- older versions read: yes
--
-- A proposed case is waiting until its fit check finds nothing missing, or the operator accepts it. An
-- older version reads a waiting case and leaves it out of its suites, as it does a retired one.
ALTER TABLE eval_cases DROP CONSTRAINT eval_cases_status_check;
ALTER TABLE eval_cases ADD CONSTRAINT eval_cases_status_check CHECK (status IN ('active', 'waiting', 'retired'));

-- The fit check of the case's text as it now is: its state (pending, running, checked, unchecked,
-- error), what the judge listed as needed by the held-out files and not stated in the text, why it
-- did not run, the models, the judge's run, and the revision of the text it checked. revision counts
-- the operator's edits of the text, so a check of an older text is not taken for the new one.
ALTER TABLE eval_cases ADD COLUMN fit jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE eval_cases ADD COLUMN revision integer NOT NULL DEFAULT 1;

-- The output of the eval run's failed held-out checks, each round's, redacted and cut to its last
-- 64 KB: for the operator, never for the builder.
ALTER TABLE eval_runs ADD COLUMN failed_output text;
