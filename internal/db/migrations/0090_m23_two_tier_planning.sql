-- older versions read: yes

-- Milestone M23 (design 2.0, 6.8, 7.2, 7.14, 15.3): Two-tier Planning Studio
-- (.crew/specs/000-foundation.md gated by Project OK + .crew/specs/NNN-<slug>.md
-- supporting autonomous @agent -> @planner deliberation and DEC-* records).

CREATE TABLE IF NOT EXISTS specs (
	id                bigserial PRIMARY KEY,
	project_id        uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	number            integer NOT NULL CHECK (number >= 0),
	slug              text NOT NULL,
	spec_key          text NOT NULL,
	path              text NOT NULL,
	tier              text NOT NULL CHECK (tier IN ('foundation', 'feature')),
	title             text NOT NULL,
	body_markdown     text NOT NULL DEFAULT '',
	state             text NOT NULL DEFAULT 'draft' CHECK (state IN ('waiting_project_ok', 'draft', 'deliberating', 'escalated', 'approved', 'discarded')),
	approval_mode     text NOT NULL DEFAULT 'manual' CHECK (approval_mode IN ('project_ok', 'autonomous', 'manual')),
	decided_by        text NOT NULL DEFAULT '',
	escalation_reason text NOT NULL DEFAULT '',
	touched_paths     text[] NOT NULL DEFAULT '{}',
	alters_foundation boolean NOT NULL DEFAULT false,
	thread_ref        text NOT NULL DEFAULT '',
	factory_task_id   uuid REFERENCES factory_tasks(id) ON DELETE SET NULL,
	proposed_tickets  jsonb NOT NULL DEFAULT '[]'::jsonb,
	emitted_keys      text[] NOT NULL DEFAULT '{}',
	created_by        text NOT NULL DEFAULT 'crew',
	approved_at       timestamptz,
	created_at        timestamptz NOT NULL DEFAULT now(),
	updated_at        timestamptz NOT NULL DEFAULT now(),
	UNIQUE (project_id, number),
	UNIQUE (project_id, path),
	UNIQUE (project_id, spec_key)
);

CREATE INDEX IF NOT EXISTS specs_project_state_idx
	ON specs (project_id, tier, state, number);

CREATE TABLE IF NOT EXISTS spec_questions (
	id           bigserial PRIMARY KEY,
	project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	spec_id      bigint NOT NULL REFERENCES specs(id) ON DELETE CASCADE,
	ticket_id    bigint REFERENCES tickets(id) ON DELETE SET NULL,
	asking_role  text NOT NULL,
	asking_run   uuid,
	thread_ref   text NOT NULL DEFAULT '',
	question     text NOT NULL,
	context_note text NOT NULL DEFAULT '',
	hop_count    integer NOT NULL DEFAULT 1 CHECK (hop_count >= 1),
	planner_run  uuid,
	answer       text NOT NULL DEFAULT '',
	state        text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'processing', 'answered', 'escalated')),
	created_at   timestamptz NOT NULL DEFAULT now(),
	answered_at  timestamptz
);

CREATE INDEX IF NOT EXISTS spec_questions_open_idx
	ON spec_questions (project_id, state, id);

CREATE TABLE IF NOT EXISTS spec_decisions (
	id                bigserial PRIMARY KEY,
	project_id        uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	number            integer NOT NULL CHECK (number >= 1),
	key               text NOT NULL,
	spec_id           bigint NOT NULL REFERENCES specs(id) ON DELETE CASCADE,
	question_id       bigint REFERENCES spec_questions(id) ON DELETE SET NULL,
	decision_id       bigint REFERENCES decisions(id) ON DELETE SET NULL,
	asking_role       text NOT NULL DEFAULT '',
	decided_by        text NOT NULL,
	question          text NOT NULL,
	choice            text NOT NULL,
	alternatives      jsonb NOT NULL DEFAULT '[]'::jsonb,
	rationale         text NOT NULL,
	state             text NOT NULL DEFAULT 'approved' CHECK (state IN ('approved', 'pending_operator', 'rejected', 'overridden')),
	escalation_reason text NOT NULL DEFAULT '',
	artifact_path     text NOT NULL DEFAULT '',
	created_at        timestamptz NOT NULL DEFAULT now(),
	updated_at        timestamptz NOT NULL DEFAULT now(),
	UNIQUE (project_id, number),
	UNIQUE (project_id, key)
);

CREATE INDEX IF NOT EXISTS spec_decisions_spec_idx
	ON spec_decisions (spec_id, number);
