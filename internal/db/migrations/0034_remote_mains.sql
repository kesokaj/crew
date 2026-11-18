-- Design 0.43, M10: work that came in from outside (6.6, 7.12). Crew fetches its remote's main on a
-- schedule and before each merge. A main that moved there is taken as the new base and checked; one
-- that changes files that are code waits for the operator; one that went another way than Crew's is
-- merged with it and checked first. What it found is kept, with the fix ticket it made.
ALTER TABLE projects ADD COLUMN fetched_at timestamptz;
ALTER TABLE projects ADD COLUMN fetch_detail text;

CREATE TABLE remote_mains (
    id          bigserial PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    -- the remote's main, and Crew's main when it was found
    remote      text        NOT NULL,
    base        text        NOT NULL,
    -- ahead: the remote's main follows Crew's; diverged: each has commits the other has not
    kind        text        NOT NULL CHECK (kind IN ('ahead', 'diverged')),
    -- the commit that is checked: the remote's main, or Crew's merge of both
    commit      text,
    -- held waits for the operator, who takes it (taken, which Crew then checks) or leaves it (refused)
    state       text        NOT NULL CHECK (state IN ('held', 'taken', 'checking', 'passed', 'failed', 'conflict', 'error', 'refused')),
    -- the files that are code (held), or that conflict
    files       text[]      NOT NULL DEFAULT '{}',
    results     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    detail      text,
    tries       integer     NOT NULL DEFAULT 0,
    ticket      bigint REFERENCES tickets (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz,
    UNIQUE (project, remote, base)
);
