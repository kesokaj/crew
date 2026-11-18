-- Design 0.48 (M14). Releases of Crew and the updater's requests (16.1 to 16.5, 15.3).
-- older versions read: yes
--
-- A release is a folder on the machine, found by the server and recorded here with its notice, its
-- hold, and when it was installed. The kind is decided by the running version from the files the
-- release changes (16.3); an agent cannot set it, since no tool of the Crew MCP writes this table.
CREATE TABLE releases (
    version         text        PRIMARY KEY,
    commit          text        NOT NULL,
    -- The sha256 of release.json, which names every image file by its own hash: what was tested.
    manifest_sha256 text        NOT NULL,
    manifest        jsonb       NOT NULL,
    built_at        timestamptz NOT NULL,
    found_at        timestamptz NOT NULL DEFAULT now(),
    -- text installs by itself after its notice; code and setup wait for Install (16.3).
    kind            text        NOT NULL CHECK (kind IN ('text', 'code', 'setup')),
    -- The paths it changes that are on the list of protected parts (16.8).
    protected       text[]      NOT NULL DEFAULT '{}',
    changed         integer     NOT NULL DEFAULT 0,
    -- A build by hand: no gate, given to the updater by root on the machine only (16.10).
    by_hand         boolean     NOT NULL DEFAULT false,
    -- gate.json, when the gate passed; null for a release that has none.
    gate            jsonb,
    notice_at       timestamptz,
    install_after   timestamptz,
    held            boolean     NOT NULL DEFAULT false,
    held_why        text,
    replaced_by     text,
    -- The last request to the updater for it, and what came of it.
    state           text        NOT NULL DEFAULT 'new'
                    CHECK (state IN ('new', 'waiting', 'installing', 'running', 'kept', 'failed', 'removed')),
    failure         text,
    installed_at    timestamptz,
    -- The private copy on GitHub (16.9): the tag, the release page, and the images, each with the
    -- time it was made; what is missing is tried again.
    published       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    publish_error   text,
    publish_tries   integer     NOT NULL DEFAULT 0,
    publish_next    timestamptz
);

-- The updater keeps its own log on the machine, so it needs neither the server nor the database
-- (16.8). The server copies each request here when it reads the updater's status: the Versions
-- screen and the history read this table.
CREATE TABLE update_requests (
    id           bigint      PRIMARY KEY,
    version      text        NOT NULL,
    from_version text,
    action       text        NOT NULL CHECK (action IN ('install', 'rollback', 'check')),
    asked_by     text        NOT NULL,
    state        text        NOT NULL CHECK (state IN ('asked', 'running', 'done', 'failed', 'switched_back', 'stopped')),
    step         integer     NOT NULL DEFAULT 0,
    steps        jsonb       NOT NULL DEFAULT '[]'::jsonb,
    error        text,
    asked_at     timestamptz NOT NULL,
    ended_at     timestamptz
);
CREATE INDEX update_requests_by_version ON update_requests (version, id);
