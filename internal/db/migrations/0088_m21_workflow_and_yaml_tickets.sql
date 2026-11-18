-- Schema step 88 (M21, design 2.0): workflow_defs (WORKFLOW.md pinned to main) and YAML ticket contract columns.
-- older versions read: yes

CREATE TABLE IF NOT EXISTS workflow_defs (
    project_id      uuid        PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    main_sha        text        NOT NULL DEFAULT '',
    raw_markdown    text        NOT NULL DEFAULT '',
    frontmatter     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    harness         text        NOT NULL DEFAULT 'agy',
    max_tries       integer     NOT NULL DEFAULT 3 CHECK (max_tries >= 1 AND max_tries <= 20),
    max_agent_hops  integer     NOT NULL DEFAULT 3 CHECK (max_agent_hops >= 1 AND max_agent_hops <= 20),
    feature_specs   text        NOT NULL DEFAULT 'autonomous'
                    CHECK (feature_specs IN ('autonomous', 'manual')),
    synced_at       timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE tickets
    ADD COLUMN IF NOT EXISTS spec_ref text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS max_tries integer NOT NULL DEFAULT 3 CHECK (max_tries >= 1 AND max_tries <= 20),
    ADD COLUMN IF NOT EXISTS tries_used integer NOT NULL DEFAULT 0 CHECK (tries_used >= 0),
    ADD COLUMN IF NOT EXISTS assigned_agent text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS budget_tokens bigint NOT NULL DEFAULT 0 CHECK (budget_tokens >= 0);
