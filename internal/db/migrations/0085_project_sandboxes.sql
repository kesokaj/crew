-- 0085_project_sandboxes.sql: one warm Kata microVM + one agy acp server per project and sealed home snapshots (design 1.37, 13.3, 15.3).
-- older versions read: yes

CREATE TABLE IF NOT EXISTS project_sandboxes (
    project_id      uuid        PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    sandbox_id      text        NOT NULL DEFAULT '',
    state           text        NOT NULL DEFAULT 'stopped'
                    CHECK (state IN ('stopped', 'running', 'paused')),
    home_path       text        NOT NULL,
    image_tag       text        NOT NULL DEFAULT '',
    snapshot_id     text        NOT NULL DEFAULT '',
    active_sessions integer     NOT NULL DEFAULT 0 CHECK (active_sessions >= 0),
    last_active_at  timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS project_snapshots (
    id           text        PRIMARY KEY CHECK (id <> '' AND length(id) <= 120),
    project_id   uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    label        text        NOT NULL CHECK (length(label) <= 240),
    archive_path text        NOT NULL,
    sha256       text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes   bigint      NOT NULL CHECK (size_bytes >= 0),
    image_tag    text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS project_snapshots_project_idx
    ON project_snapshots(project_id, created_at DESC);
