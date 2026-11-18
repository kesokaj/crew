-- Design 0.44 (M11). A ticket that cannot get through goes to the lead, who looks again (7.7): once
-- a new approach, which gives the ticket two more fix rounds and is counted here with its time, so
-- the tries before it no longer count; otherwise the ticket is cancelled and replaced. The stopped
-- subtasks a new approach leaves behind become superseded: they wait for nothing.
ALTER TABLE tickets ADD COLUMN approaches int NOT NULL DEFAULT 0;
ALTER TABLE tickets ADD COLUMN approach_at timestamptz;
