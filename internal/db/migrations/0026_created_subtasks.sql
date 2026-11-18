-- M9: subtasks an agent creates on its own ticket (7.5, 7.11). created_by names the pipeline, the
-- backend, or the role and the run that asked for it, and created_run is that run. added marks a
-- subtask that counts toward limits.max_added_subtasks. what, done_when, and check_cmd are what the
-- creating agent wrote for it; after holds the numbers of the subtasks it comes after, and touches the
-- files or folders it may change. joined_at is when the backend joined a parallel subtask's branch
-- into its ticket's branch.
ALTER TABLE subtasks
    ADD COLUMN created_by  text NOT NULL DEFAULT 'pipeline',
    ADD COLUMN created_run uuid,
    ADD COLUMN added       boolean NOT NULL DEFAULT false,
    ADD COLUMN what        text,
    ADD COLUMN done_when   text,
    ADD COLUMN check_cmd   text,
    ADD COLUMN joined_at   timestamptz;
CREATE INDEX subtasks_queued ON subtasks (ticket) WHERE state = 'queued';

-- A parallel subtask's own check runs on its branch (part), and again on its join into the ticket's
-- branch (join), each in a check sandbox (7.11).
ALTER TABLE check_runs DROP CONSTRAINT check_runs_purpose_check;
ALTER TABLE check_runs ADD CONSTRAINT check_runs_purpose_check CHECK (purpose IN ('base', 'exit', 'test', 'merge', 'part', 'join'));
