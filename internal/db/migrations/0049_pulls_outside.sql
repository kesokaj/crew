-- Design 0.47 (M13). A pull request changed or merged on GitHub by someone else (21, 7.12): Crew reads
-- each open one after every fetch. outside says it was merged or closed there, by outside_by; a merged
-- one makes its ticket Done, and its checks run on main once the merge is there (outside_check), with
-- a fix ticket when they fail (outside_ticket); outside_done says that is over. diverged is a head of
-- the branch on GitHub that went another way than Crew's, and diverged_kept the one the operator
-- answered Keep Crew's for.
ALTER TABLE pull_requests ADD COLUMN outside text CHECK (outside IN ('merged', 'closed'));
ALTER TABLE pull_requests ADD COLUMN outside_by text;
ALTER TABLE pull_requests ADD COLUMN outside_head text;
ALTER TABLE pull_requests ADD COLUMN outside_check bigint REFERENCES check_runs (id) ON DELETE SET NULL;
ALTER TABLE pull_requests ADD COLUMN outside_ticket bigint REFERENCES tickets (id) ON DELETE SET NULL;
ALTER TABLE pull_requests ADD COLUMN outside_done boolean NOT NULL DEFAULT false;
ALTER TABLE pull_requests ADD COLUMN diverged text;
ALTER TABLE pull_requests ADD COLUMN diverged_kept text;

ALTER TABLE check_runs DROP CONSTRAINT check_runs_purpose_check;
ALTER TABLE check_runs ADD CONSTRAINT check_runs_purpose_check
    CHECK (purpose IN ('base', 'exit', 'test', 'merge', 'part', 'join', 'judge', 'e2e', 'stage', 'stage_judge', 'outside'));
