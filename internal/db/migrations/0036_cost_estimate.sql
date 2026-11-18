-- Design 0.44 (M11). A run's cost is estimated from its tokens when its CLI reports none, as agy does
-- (12.7): the tokens read from the cache are kept, since they have a price of their own, and a run
-- says how its cost was found. Runs from before are estimated once by the server when it starts.
ALTER TABLE runs ADD COLUMN tokens_cached bigint NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN cost_basis text CHECK (cost_basis IN ('reported', 'estimated', 'unpriced'));
