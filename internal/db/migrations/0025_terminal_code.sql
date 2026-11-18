-- M9: a main from the project terminal that changes files that are code for the agent CLI, or
-- workflows, is held for the operator like any other, with those files named (13.8, 13.11).
ALTER TABLE terminal_mains ADD COLUMN code text[] NOT NULL DEFAULT '{}';
