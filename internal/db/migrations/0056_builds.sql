-- Design 0.51 (M16, P3). The builds of a release Crew makes of its own project (16.2 step 4).
-- older versions read: yes
--
-- One row per build, with the end of its output, which Versions shows. One build runs at a time, and
-- a version is built once: a failed build leaves its number free. An older version reads nothing of
-- it.
CREATE TABLE builds (
    id          bigserial PRIMARY KEY,
    version     text        NOT NULL,
    commit      text        NOT NULL,
    asked_by    text        NOT NULL,
    state       text        NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'done', 'failed')),
    log         text        NOT NULL DEFAULT '',
    error       text,
    started_at  timestamptz NOT NULL DEFAULT now(),
    ended_at    timestamptz
);
CREATE UNIQUE INDEX builds_one_running ON builds ((true)) WHERE state = 'running';
CREATE UNIQUE INDEX builds_version_once ON builds (version) WHERE state <> 'failed';
