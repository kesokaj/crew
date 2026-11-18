-- 0084_gate_auto_install.sql: single-action gate auto-install and global harness files (design 1.35, 10.6, 16.2, 16.5).
-- older versions read: yes

ALTER TABLE gates
    ADD COLUMN IF NOT EXISTS install_when_done boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS crew_harness_files (
    path        text        PRIMARY KEY CHECK (path <> '' AND length(path) <= 240),
    content     text        NOT NULL,
    updated_by  text        NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
