-- M3, from the reviews (design 8.2, 12.4).

-- When the run's current turn began: the time limit and the silence clock are a turn's, as crew-run
-- applies the limit, not the run's whole life with its idle gaps.
ALTER TABLE runs ADD COLUMN turn_started_at timestamptz;
UPDATE runs SET turn_started_at = started_at;
ALTER TABLE runs ALTER COLUMN turn_started_at SET NOT NULL, ALTER COLUMN turn_started_at SET DEFAULT now();

-- How many times Crew continued the subtask by itself, after a retry, a restart, a sign-in, or a
-- limit. A subtask that has continued by itself this often waits for the operator, so nothing a
-- sandbox writes can start runs without end.
ALTER TABLE subtasks ADD COLUMN auto_continues integer NOT NULL DEFAULT 0;
