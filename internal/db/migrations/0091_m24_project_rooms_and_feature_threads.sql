-- older versions read: yes

-- Milestone M24 (design 2.0, 6.8, 11.10, 13.8, 15.3, 20.4): Unified #<project> channel,
-- per-feature threads (feat/<NNN>-<slug>), provenance fencing, and max_agent_hops bounds (R214, R217).

CREATE TABLE IF NOT EXISTS chat_rooms (
	id         bigserial PRIMARY KEY,
	project_id uuid NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
	channel    text NOT NULL,
	topic      text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO chat_rooms (project_id, channel, topic)
SELECT id, '#' || name, goal FROM projects
ON CONFLICT (project_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS chat_threads (
	id              bigserial PRIMARY KEY,
	room_id         bigint NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
	project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	thread_key      text NOT NULL,
	title           text NOT NULL,
	spec_id         bigint REFERENCES specs(id) ON DELETE SET NULL,
	root_message_id bigint,
	ticket_keys     text[] NOT NULL DEFAULT '{}',
	state           text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved', 'archived')),
	created_at      timestamptz NOT NULL DEFAULT now(),
	updated_at      timestamptz NOT NULL DEFAULT now(),
	UNIQUE (project_id, thread_key)
);

CREATE INDEX IF NOT EXISTS chat_threads_room_idx
	ON chat_threads (room_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS chat_messages (
	id              bigserial PRIMARY KEY,
	room_id         bigint NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
	thread_id       bigint REFERENCES chat_threads(id) ON DELETE CASCADE,
	project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	task_id         uuid REFERENCES factory_tasks(id) ON DELETE SET NULL,
	ticket_id       bigint REFERENCES tickets(id) ON DELETE SET NULL,
	run_id          uuid,
	sender_role     text NOT NULL,
	sender_handle   text NOT NULL,
	mentions        text[] NOT NULL DEFAULT '{}',
	hop_count       integer NOT NULL DEFAULT 0 CHECK (hop_count >= 0),
	fenced          boolean NOT NULL DEFAULT false,
	body            text NOT NULL,
	fenced_body     text NOT NULL,
	actionable      boolean NOT NULL DEFAULT false,
	spawned_session uuid,
	created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS chat_messages_room_idx
	ON chat_messages (room_id, thread_id, id ASC);
