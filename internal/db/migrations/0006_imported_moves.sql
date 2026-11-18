-- M4, from the reviews: a move and its check results that came in with an imported project say so,
-- so they are never taken for this machine's own (6.2).
ALTER TABLE stage_moves   ADD COLUMN imported boolean NOT NULL DEFAULT false;
ALTER TABLE check_results ADD COLUMN imported boolean NOT NULL DEFAULT false;
