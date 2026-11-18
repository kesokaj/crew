-- M8, the operator's answer to M7's open item 1: a main the project terminal pushes in edit mode waits
-- for the operator's confirmation, with its diff, before it becomes the base. Until then Crew's main
-- stays as it was, and the git endpoint serves it; the pushed commit is kept in the canonical
-- repository under refs/crew/held/<id>.
CREATE TABLE terminal_mains (
    id          bigserial   PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    repo        text        NOT NULL,
    old         text        NOT NULL,
    new         text        NOT NULL,
    files       text[]      NOT NULL DEFAULT '{}',
    pushed_by   text        NOT NULL,
    pushed_at   timestamptz NOT NULL DEFAULT now(),
    state       text        NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'taken', 'refused', 'failed')),
    detail      text,
    decided_by  text,
    decided_at  timestamptz
);
CREATE INDEX terminal_mains_waiting ON terminal_mains (project) WHERE state = 'waiting';
