-- Design 0.43, M10: a project's own stage (7.4, R101). Its agent's verdict is an entry of the kind
-- stage, with a result and the stage it is about; one a run. Crew's run of the stage's command
-- checks is a check run of the purpose stage, and a judge's answer to its judge: checks one of the
-- purpose stage_judge.
ALTER TABLE records ADD COLUMN stage text;
ALTER TABLE records DROP CONSTRAINT records_kind_check;
ALTER TABLE records ADD CONSTRAINT records_kind_check
    CHECK (kind IN ('note', 'review', 'test', 'comment', 'e2e', 'verify', 'stage'));
ALTER TABLE records DROP CONSTRAINT records_result_kind_check;
ALTER TABLE records ADD CONSTRAINT records_result_kind_check CHECK ((kind IN ('test', 'e2e', 'verify', 'stage')) = (result IS NOT NULL));
ALTER TABLE records ADD CONSTRAINT records_stage_kind_check CHECK ((kind = 'stage') = (stage IS NOT NULL));
DROP INDEX records_one_entry_per_run;
CREATE UNIQUE INDEX records_one_entry_per_run ON records (run, kind)
    WHERE kind IN ('review', 'test', 'e2e', 'verify', 'stage') AND run IS NOT NULL;

ALTER TABLE check_runs DROP CONSTRAINT check_runs_purpose_check;
ALTER TABLE check_runs ADD CONSTRAINT check_runs_purpose_check
    CHECK (purpose IN ('base', 'exit', 'test', 'merge', 'part', 'join', 'judge', 'e2e', 'stage', 'stage_judge'));
