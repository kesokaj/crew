-- M8, after its reviews.
-- A name a CLI refused in a run is passed over for a while, then tried again: a run's report is its
-- own account (12.4), so it never takes a model away from every project for good.
ALTER TABLE models ADD COLUMN refused_at timestamptz;
UPDATE models SET offered = true, gone_at = NULL, refused_at = now() WHERE refused AND added_by = 'cli' AND read_at > coalesce(gone_at, read_at - interval '1 second');
-- A held main is claimed by the one decision that takes or refuses it.
ALTER TABLE terminal_mains DROP CONSTRAINT terminal_mains_state_check;
ALTER TABLE terminal_mains ADD CONSTRAINT terminal_mains_state_check CHECK (state IN ('waiting', 'deciding', 'taken', 'refused', 'failed'));
