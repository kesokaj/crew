-- M7: the project terminal (design 13.11).

-- The one project terminal of a project while it is open: its microVM, its mode, and when it was last
-- typed into, which the idle reaper reads. Opening, switching the mode, and closing are rows in
-- changes, so they are in the project's history.
CREATE TABLE project_terminals (
    project     uuid PRIMARY KEY REFERENCES projects (id),
    sandbox     text        NOT NULL REFERENCES sandboxes (id),
    mode        text        NOT NULL DEFAULT 'diagnostics' CHECK (mode IN ('diagnostics', 'edit')),
    opened_at   timestamptz NOT NULL DEFAULT now(),
    input_at    timestamptz NOT NULL DEFAULT now()
);

-- What was typed and printed in a terminal, the project terminal's or one inside a run's microVM:
-- kept 180 days (13.11), then removed.
CREATE TABLE terminal_log (
    id          bigserial PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    sandbox     text        NOT NULL,
    run         uuid REFERENCES runs (id),
    dir         text        NOT NULL CHECK (dir IN ('in', 'out')),
    data        bytea       NOT NULL,
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX terminal_log_by_project ON terminal_log (project, id);
CREATE INDEX terminal_log_by_age ON terminal_log (at);
