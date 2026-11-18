-- Design 0.47 (M13). A project's setup that fails (21, 12.4, 13.5): after two runs in a row ended at
-- their setup, Crew runs the setup on main in a check sandbox. One that fails there too makes the fix
-- ticket, which the runs that need the setup wait for; one that passes there says the tickets' own
-- branches broke it, and their failures count as their stops. Failures of runs that started before
-- through are judged by this row.
CREATE TABLE setup_checks (
    id         bigserial   PRIMARY KEY,
    project    uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    state      text        NOT NULL DEFAULT 'checking' CHECK (state IN ('checking', 'passed', 'failed', 'error')),
    commit     text        NOT NULL,
    through    timestamptz NOT NULL,
    detail     text,
    ticket     bigint      REFERENCES tickets (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    -- acted_at is when the pipeline acted on the decision: the fix ticket made, or the failures counted.
    acted_at   timestamptz
);
CREATE INDEX setup_checks_project ON setup_checks (project, id);
