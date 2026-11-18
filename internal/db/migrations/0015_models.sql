-- M8: the models each CLI offers, read as the CLI lists them (12.3), and the limits of each group of
-- models (12.7).

-- One row per CLI and name as the CLI takes it. model is the name without its effort; efforts are
-- what the name offers: one for an agy name that holds it, several for a claude name that takes a
-- flag (in_name false). gone_at is when a CLI stopped listing it; the row stays, so old runs still show
-- what they used. refused says the CLI refused the name in a run.
ALTER TABLE models
    ADD COLUMN model      text,
    ADD COLUMN in_name    boolean     NOT NULL DEFAULT false,
    ADD COLUMN display    text,
    ADD COLUMN resolved   text,
    ADD COLUMN first_seen timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN gone_at    timestamptz,
    ADD COLUMN refused    boolean     NOT NULL DEFAULT false;
UPDATE models SET model = name;
ALTER TABLE models ALTER COLUMN model SET NOT NULL;

-- Reading a CLI's list can fail while its login works: after two failures in a row the operator gets
-- news (12.3). models_read_at is the last list that was read.
ALTER TABLE cli_logins
    ADD COLUMN models_read_at   timestamptz,
    ADD COLUMN models_failures  integer NOT NULL DEFAULT 0,
    ADD COLUMN models_error     text,
    ADD COLUMN models_failed_at timestamptz;

-- The quota guard's state (12.7): one row for each window of each group of models of a CLI, and of a
-- single model with a limit of its own (grp model:opus). reached_until is set while the CLI refuses
-- work in it: to its reset, or 15 minutes on when no reset is known, after which Crew tries again.
CREATE TABLE quota (
    cli           text        NOT NULL,
    grp           text        NOT NULL,
    win           text        NOT NULL,
    used          real        NOT NULL DEFAULT 0,
    resets_at     timestamptz,
    reached_until timestamptz,
    reached_at    timestamptz,
    source        text        NOT NULL,
    read_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cli, grp, win)
);
