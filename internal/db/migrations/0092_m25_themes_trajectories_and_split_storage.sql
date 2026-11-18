-- older versions read: yes

-- Milestone M25 (design 2.0, 5.1, 8.3, 8.7, 12.11, 20.4): Runtime 40-theme operator prefs,
-- Trajectory Store FTS indexing, BlobStore archive metadata, and split storage settings (R220).

CREATE TABLE IF NOT EXISTS operator_prefs (
	email      text PRIMARY KEY,
	theme      text NOT NULL DEFAULT 'catppuccin',
	dark       boolean NOT NULL DEFAULT true,
	direction  text NOT NULL DEFAULT 'A',
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS trajectories (
	run_id           uuid PRIMARY KEY,
	project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	ticket_id        bigint REFERENCES tickets(id) ON DELETE SET NULL,
	task_id          uuid REFERENCES factory_tasks(id) ON DELETE SET NULL,
	subtask_id       bigint,
	role             text NOT NULL,
	cli              text NOT NULL DEFAULT 'agy',
	model            text NOT NULL DEFAULT '',
	effort           text NOT NULL DEFAULT '',
	tool_verbs       text[] NOT NULL DEFAULT '{}',
	touched_paths    text[] NOT NULL DEFAULT '{}',
	events_count     bigint NOT NULL DEFAULT 0,
	turns_count      integer NOT NULL DEFAULT 0,
	tokens_total     bigint NOT NULL DEFAULT 0,
	duration_seconds integer NOT NULL DEFAULT 0,
	outcome          text NOT NULL DEFAULT 'running',
	blob_key         text NOT NULL DEFAULT '',
	search_text      text NOT NULL DEFAULT '',
	search_tsv       tsvector GENERATED ALWAYS AS (to_tsvector('english', search_text)) STORED,
	started_at       timestamptz NOT NULL DEFAULT now(),
	ended_at         timestamptz
);

CREATE INDEX IF NOT EXISTS trajectories_project_started_idx
	ON trajectories (project_id, started_at DESC);
CREATE INDEX IF NOT EXISTS trajectories_fts_idx
	ON trajectories USING GIN (search_tsv);

CREATE TABLE IF NOT EXISTS blob_objects (
	blob_key     text PRIMARY KEY,
	project_id   uuid REFERENCES projects(id) ON DELETE SET NULL,
	run_id       uuid,
	kind         text NOT NULL CHECK (kind IN ('trajectory', 'snapshot', 'e2e_media', 'release')),
	driver       text NOT NULL CHECK (driver IN ('localfs', 'gcs')),
	sha256       text NOT NULL,
	size_bytes   bigint NOT NULL CHECK (size_bytes >= 0),
	content_type text NOT NULL DEFAULT 'application/zstd',
	created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS blob_objects_project_idx
	ON blob_objects (project_id, created_at DESC);

CREATE TABLE IF NOT EXISTS storage_settings (
	id            boolean PRIMARY KEY DEFAULT true CHECK (id),
	state_db_mode text NOT NULL DEFAULT 'postgres' CHECK (state_db_mode IN ('postgres', 'sqlite')),
	blob_driver   text NOT NULL DEFAULT 'localfs' CHECK (blob_driver IN ('localfs', 'gcs')),
	gcs_bucket    text NOT NULL DEFAULT '',
	updated_by    text NOT NULL DEFAULT '',
	updated_at    timestamptz NOT NULL DEFAULT now()
);

INSERT INTO storage_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
