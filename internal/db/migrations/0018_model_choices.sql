-- M8: Crew's automatic choice of an agent's entry and effort (12.3).

-- Every choice of a model that Crew made or recommends, with the numbers behind it and what followed.
-- kind: change (Crew put another entry or effort first), back (Crew went back from a change that
-- measured worse, or the operator pressed Go back), recommend (what Crew would choose for an agent the
-- operator set). state: watching or kept for a change; done for a back; open, used, or dismissed for
-- a recommendation. An entry gone back from is neither chosen nor tried until retry_days have passed.
CREATE TABLE model_choices (
    id          bigserial   PRIMARY KEY,
    role        text        NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('change', 'back', 'recommend')),
    reason      text        NOT NULL,
    from_entry  text        NOT NULL,
    from_effort text        NOT NULL,
    to_entry    text        NOT NULL,
    to_effort   text        NOT NULL,
    numbers     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    state       text        NOT NULL,
    who         text        NOT NULL DEFAULT 'crew',
    hide_until  timestamptz,
    at          timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz
);
CREATE INDEX model_choices_by_role ON model_choices (role, at);

-- The comparisons the operator started: one agent, its own entry and another, until each has
-- compare_runs units or the operator stops it.
CREATE TABLE comparisons (
    id          bigserial   PRIMARY KEY,
    role        text        NOT NULL,
    own_entry   text        NOT NULL,
    own_effort  text        NOT NULL,
    other_entry text        NOT NULL,
    other_effort text       NOT NULL,
    state       text        NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'ended', 'stopped')),
    numbers     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    started_at  timestamptz NOT NULL DEFAULT now(),
    ended_at    timestamptz
);
CREATE UNIQUE INDEX comparisons_one_running ON comparisons (role) WHERE state = 'running';
