-- Design 0.65 (M17, P3). Two microVMs that do not boot hold new runs; the database written again (21).
-- older versions read: yes
--
-- Every start that reached Docker's counts (D10): a microVM that did not boot adds one to
-- boot_failures, a boot sets it to 0, and at two in a row boot_failed holds Crew with the error, in
-- the row every start asks, until a probe boots or the operator resumes. db_unwritable_from and _to
-- are the time the database could not be written, kept once it can again, for the inbox, until the
-- operator has read it. An older version reads none of the five columns, so it holds nothing for
-- them.
ALTER TABLE crew_state
    ADD COLUMN boot_failed        text,
    ADD COLUMN boot_failed_at     timestamptz,
    ADD COLUMN boot_failures      int NOT NULL DEFAULT 0,
    ADD COLUMN db_unwritable_from timestamptz,
    ADD COLUMN db_unwritable_to   timestamptz;
