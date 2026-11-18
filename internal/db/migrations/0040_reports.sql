-- Design 0.44 (M11). A project's final report (7.2, 9.8): the lead's sections, and the facts Crew
-- counts itself. A project that runs again and is Done again gets the next number. A report the lead
-- could not write keeps the facts, with its sections marked missing and why.
CREATE TABLE reports (
    id          bigserial   PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    number      int         NOT NULL,
    sections    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    facts       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    missing     text,
    run         uuid REFERENCES runs (id),
    at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project, number)
);
