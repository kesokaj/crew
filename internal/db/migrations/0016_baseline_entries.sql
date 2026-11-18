-- M8: the baseline's entries each agent has been given on this machine (12.3). Install copies the
-- baseline's lists into agents.roles; a later release adds only an entry this table does not hold,
-- so an entry the operator took out does not come back.
CREATE TABLE baseline_entries (
    role       text        NOT NULL,
    entry      text        NOT NULL,
    first_seen timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role, entry)
);
