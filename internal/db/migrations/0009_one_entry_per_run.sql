-- M6, after its security review: a run writes at most one review and one test entry, which the
-- database holds, so two calls at once cannot both pass the tool's check.
CREATE UNIQUE INDEX records_one_entry_per_run ON records (run, kind) WHERE kind IN ('review', 'test') AND run IS NOT NULL;

-- Why the work around a ticket starts nothing more for it, such as a run that stopped twice: the
-- pipeline's own reason, beside the board's waiting, which says why the ticket does not move now and
-- which the board writes on every pass.
ALTER TABLE tickets ADD COLUMN stuck text;
