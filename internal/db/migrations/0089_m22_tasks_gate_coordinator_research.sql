-- older versions read: yes

-- Milestone M22 (design 2.0, 5.1, 7.14, 15.3): Tasks API off-board work ledger (R217),
-- coordinator sequencing support (R211), and clean-context @researcher briefs (R213).

CREATE TABLE IF NOT EXISTS factory_tasks (
	id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	number        integer NOT NULL,
	key           text NOT NULL,
	kind          text NOT NULL CHECK (kind IN ('spec', 'research', 'dream', 'coordinator', 'chat_action')),
	title         text NOT NULL,
	role          text NOT NULL,
	state         text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed', 'cancelled')),
	ticket_id     bigint REFERENCES tickets(id) ON DELETE SET NULL,
	spec_ref      text NOT NULL DEFAULT '',
	origin_room   text NOT NULL DEFAULT '',
	yaml_contract text NOT NULL DEFAULT '',
	session_id    uuid,
	subtask_id    bigint REFERENCES subtasks(id) ON DELETE SET NULL,
	sandbox_id    text NOT NULL DEFAULT '',
	tries_used    integer NOT NULL DEFAULT 0,
	max_tries     integer NOT NULL DEFAULT 3,
	budget_tokens bigint NOT NULL DEFAULT 0,
	tokens_in     bigint NOT NULL DEFAULT 0,
	tokens_out    bigint NOT NULL DEFAULT 0,
	artifact_path text NOT NULL DEFAULT '',
	summary       text NOT NULL DEFAULT '',
	created_by    text NOT NULL DEFAULT 'crew',
	created_at    timestamptz NOT NULL DEFAULT now(),
	updated_at    timestamptz NOT NULL DEFAULT now(),
	UNIQUE (project_id, number),
	UNIQUE (project_id, key)
);

CREATE INDEX IF NOT EXISTS factory_tasks_project_state_idx
	ON factory_tasks (project_id, state, created_at DESC);

CREATE TABLE IF NOT EXISTS research_briefs (
	id                 bigserial PRIMARY KEY,
	project_id         uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	ticket_id          bigint NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
	task_id            uuid REFERENCES factory_tasks(id) ON DELETE SET NULL,
	run_id             uuid,
	tries_used         integer NOT NULL DEFAULT 0,
	wip_ref            text NOT NULL DEFAULT '',
	head_sha           text NOT NULL DEFAULT '',
	artifact_path      text NOT NULL DEFAULT '',
	diagnosis          text NOT NULL DEFAULT '',
	recommended_action text NOT NULL DEFAULT 'retry_builder' CHECK (recommended_action IN ('retry_builder', 'ask_planner', 'ask_user')),
	created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS research_briefs_ticket_idx
	ON research_briefs (ticket_id, created_at DESC);

ALTER TABLE subtasks
	ADD COLUMN IF NOT EXISTS factory_task_id uuid REFERENCES factory_tasks(id) ON DELETE SET NULL;
