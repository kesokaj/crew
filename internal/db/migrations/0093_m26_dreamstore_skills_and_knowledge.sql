-- older versions read: yes

-- Milestone M26 (design 2.0, 5.1, 6.8, 9.10, 9.11, 10.8, 12.11):
-- Memory API Dreamstore, Scoped Dreaming proposals, 4-tier Skills Registry, and Cross-Project Knowledge API.

CREATE TABLE IF NOT EXISTS dreamstore_entries (
	id                  bigserial PRIMARY KEY,
	project_id          uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	number              integer NOT NULL,
	kind                text NOT NULL CHECK (kind IN ('episodic', 'semantic', 'procedural')),
	title               text NOT NULL,
	body                text NOT NULL,
	applies_to          text[] NOT NULL DEFAULT '{all}',
	paths               text[] NOT NULL DEFAULT '{}',
	keywords            text[] NOT NULL DEFAULT '{}',
	validity_score      double precision NOT NULL DEFAULT 1.0 CHECK (validity_score >= 0.0 AND validity_score <= 1.0),
	verified_commit     text NOT NULL DEFAULT '',
	evidence_runs       text[] NOT NULL DEFAULT '{}',
	hit_pass_count      integer NOT NULL DEFAULT 0,
	contradiction_count integer NOT NULL DEFAULT 0,
	author              text NOT NULL DEFAULT 'builder',
	status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'drift', 'quarantined', 'retired')),
	retired_reason      text,
	search_tsv          tsvector GENERATED ALWAYS AS (to_tsvector('simple', title || ' ' || body)) STORED,
	created_at          timestamptz NOT NULL DEFAULT now(),
	updated_at          timestamptz NOT NULL DEFAULT now(),
	UNIQUE (project_id, number)
);

CREATE INDEX IF NOT EXISTS dreamstore_entries_project_status_idx
	ON dreamstore_entries (project_id, status, validity_score DESC, number DESC);
CREATE INDEX IF NOT EXISTS dreamstore_entries_fts_idx
	ON dreamstore_entries USING GIN (search_tsv);

CREATE TABLE IF NOT EXISTS dream_proposals (
	id               bigserial PRIMARY KEY,
	project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	number           integer NOT NULL,
	key              text NOT NULL,
	factory_task_id  uuid REFERENCES factory_tasks(id) ON DELETE SET NULL,
	run_id           uuid,
	kind             text NOT NULL CHECK (kind IN ('context_micro_adjustment', 'skill_synthesis', 'vendored_download')),
	target_path      text NOT NULL,
	skill_name       text NOT NULL DEFAULT '',
	title            text NOT NULL,
	rationale        text NOT NULL,
	proposed_content text NOT NULL,
	vendored_source  text NOT NULL DEFAULT '',
	vendored_sha     text NOT NULL DEFAULT '',
	vendored_licence text NOT NULL DEFAULT '',
	scan_verdict     text NOT NULL DEFAULT 'PASS' CHECK (scan_verdict IN ('PASS', 'BLOCKED')),
	scan_findings    jsonb NOT NULL DEFAULT '[]'::jsonb,
	promote_shared   boolean NOT NULL DEFAULT false,
	state            text NOT NULL DEFAULT 'pending_approval' CHECK (state IN ('pending_approval', 'approved', 'discarded', 'blocked')),
	decided_by       text NOT NULL DEFAULT '',
	created_at       timestamptz NOT NULL DEFAULT now(),
	updated_at       timestamptz NOT NULL DEFAULT now(),
	UNIQUE (project_id, number),
	UNIQUE (project_id, key)
);

CREATE TABLE IF NOT EXISTS skills_registry (
	id                bigserial PRIMARY KEY,
	slug              text NOT NULL,
	tier              text NOT NULL CHECK (tier IN ('base', 'shared', 'project', 'dream', 'vendored')),
	source_project_id uuid REFERENCES projects(id) ON DELETE SET NULL,
	name              text NOT NULL,
	path              text NOT NULL,
	description       text NOT NULL DEFAULT '',
	content           text NOT NULL DEFAULT '',
	source_url        text NOT NULL DEFAULT '',
	pinned_sha        text NOT NULL DEFAULT '',
	licence           text NOT NULL DEFAULT '',
	scan_verdict      text NOT NULL DEFAULT 'PASS' CHECK (scan_verdict IN ('PASS', 'BLOCKED')),
	scan_findings     jsonb NOT NULL DEFAULT '[]'::jsonb,
	validity_score    double precision NOT NULL DEFAULT 1.0,
	first_pass_delta  double precision NOT NULL DEFAULT 0.0,
	prompt_bytes      integer NOT NULL DEFAULT 0,
	token_footprint   integer NOT NULL DEFAULT 0,
	created_at        timestamptz NOT NULL DEFAULT now(),
	updated_at        timestamptz NOT NULL DEFAULT now(),
	UNIQUE (tier, slug)
);

CREATE TABLE IF NOT EXISTS skill_installs (
	skill_id        bigint NOT NULL REFERENCES skills_registry(id) ON DELETE CASCADE,
	project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	factory_task_id uuid REFERENCES factory_tasks(id) ON DELETE SET NULL,
	state           text NOT NULL DEFAULT 'active' CHECK (state IN ('pending', 'active', 'disabled')),
	synced_home     boolean NOT NULL DEFAULT false,
	installed_by    text NOT NULL DEFAULT 'operator',
	installed_at    timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (skill_id, project_id)
);

CREATE TABLE IF NOT EXISTS knowledge_entries (
	id                  bigserial PRIMARY KEY,
	number              integer NOT NULL UNIQUE,
	key                 text NOT NULL UNIQUE,
	kind                text NOT NULL CHECK (kind IN ('shared_skill', 'advisory', 'spec_pattern')),
	source_project_id   uuid REFERENCES projects(id) ON DELETE SET NULL,
	source_project_name text NOT NULL DEFAULT '',
	source_commit       text NOT NULL DEFAULT '',
	skill_name          text NOT NULL DEFAULT '',
	title               text NOT NULL,
	summary             text NOT NULL,
	body                text NOT NULL,
	fenced_body         text NOT NULL,
	tags                text[] NOT NULL DEFAULT '{}',
	validity_score      double precision NOT NULL DEFAULT 1.0,
	scrubbed            boolean NOT NULL DEFAULT true CHECK (scrubbed),
	created_by          text NOT NULL DEFAULT 'operator',
	search_tsv          tsvector GENERATED ALWAYS AS (to_tsvector('simple', title || ' ' || summary || ' ' || body)) STORED,
	created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS knowledge_entries_fts_idx
	ON knowledge_entries USING GIN (search_tsv);
