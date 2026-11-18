-- Design 0.72 (M19, phase 1). Hosting a project on the machine (19).
-- older versions read: yes
--
-- hosting is one row for each project that was ever hosted: its state, the deploy that serves and
-- its sandbox and address, a sandbox still to remove after a switch, the restarts since the deploy
-- went live, the open episode of not answering, and the head Follow main does not try again after
-- it failed (19.3, 19.5). deploys is every deploy, kept: its commit and image, why it was made and by
-- whom, the deploy whose image a roll back or a settings deploy took, the tickets in it, its state
-- and what failed, the copy of the data made before it switched and the copy a roll back put back.
-- At most one deploy of a project builds, starts or is checked at a time. A hosted project's
-- settings and secrets are one row of credentials, encrypted as every credential. An older version
-- reads none of them, so it hosts nothing.
CREATE TABLE hosting (
    project     uuid        PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    state       text        NOT NULL CHECK (state IN ('waiting_dockerfile', 'deploying', 'live', 'failing', 'stopped')),
    ticket      bigint      REFERENCES tickets (id) ON DELETE SET NULL,
    live_deploy bigint,
    sandbox     text,
    address     inet,
    retiring    text,
    restarts    integer     NOT NULL DEFAULT 0,
    down_since  timestamptz,
    failed_head text,
    port        integer,
    health      text,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE deploys (
    id          bigserial   PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    number      integer     NOT NULL CHECK (number > 0),
    commit      text        NOT NULL CHECK (commit ~ '^[0-9a-f]{40}$'),
    image       text        CHECK (image ~ '^sha256:[0-9a-f]{64}$'),
    why         text        NOT NULL CHECK (why IN ('asked', 'follow_main', 'rollback', 'hostable', 'settings')),
    asked_by    text        NOT NULL CHECK (asked_by IN ('operator', 'follow_main', 'hostable', 'settings')),
    from_deploy bigint      REFERENCES deploys (id),
    tickets     text[]      NOT NULL DEFAULT '{}',
    state       text        NOT NULL CHECK (state IN ('building', 'starting', 'checking', 'live', 'failed', 'replaced', 'stopped')),
    detail      text        NOT NULL DEFAULT '' CHECK (length(detail) <= 4000),
    data_copy   text,
    restored    text,
    started_at  timestamptz NOT NULL DEFAULT now(),
    live_at     timestamptz,
    ended_at    timestamptz,
    UNIQUE (project, number),
    CHECK ((why IN ('rollback', 'settings')) = (from_deploy IS NOT NULL))
);
CREATE UNIQUE INDEX deploys_one_running ON deploys (project) WHERE state IN ('building', 'starting', 'checking');

ALTER TABLE hosting ADD FOREIGN KEY (live_deploy) REFERENCES deploys (id);

-- A hosted project's sandbox is on a bridge of its own kind, app- with the internet or apo- without
-- (13.6); its row says which, and its size is its hosting.size.
ALTER TABLE sandboxes DROP CONSTRAINT sandboxes_mode_check;
ALTER TABLE sandboxes ADD CONSTRAINT sandboxes_mode_check
    CHECK (mode IN ('internet', 'model_only', 'lan', 'sources', 'hosted_internet', 'hosted_offline'));
