-- older versions read: yes

-- Design 2.0 (11.10, 15.3, M21-M25 hardening): Orthogonal chat message envelope,
-- per-addressee delivery state, and keyed choice prompts / interpreted decisions.

ALTER TABLE chat_messages
	ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'text' CHECK (kind IN ('text', 'event', 'choice_prompt', 'choice_response')),
	ADD COLUMN IF NOT EXISTS intent text NOT NULL DEFAULT 'inform' CHECK (intent IN ('inform', 'request', 'question')),
	ADD COLUMN IF NOT EXISTS event_type text NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS reply_to_id bigint REFERENCES chat_messages(id) ON DELETE SET NULL,
	ADD COLUMN IF NOT EXISTS interrupt boolean NOT NULL DEFAULT false,
	ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
	ADD COLUMN IF NOT EXISTS choice_prompt jsonb,
	ADD COLUMN IF NOT EXISTS choice_response jsonb,
	ADD COLUMN IF NOT EXISTS body_sha256 text NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS body_runes integer NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS chat_messages_choice_prompt_idx
	ON chat_messages (project_id, kind, id DESC);

CREATE TABLE IF NOT EXISTS chat_message_addressees (
	id               bigserial PRIMARY KEY,
	message_id       bigint NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
	project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	recipient_role   text NOT NULL,
	recipient_handle text NOT NULL,
	via              text NOT NULL CHECK (via IN ('explicit', 'body-mention', 'default-agent', 'direct')),
	delivery_mode    text NOT NULL DEFAULT 'none' CHECK (delivery_mode IN ('none', 'turn', 'steer', 'spawn')),
	delivery_state   text NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending', 'queued', 'delivered', 'suppressed', 'failed')),
	task_id          uuid REFERENCES factory_tasks(id) ON DELETE SET NULL,
	session_id       uuid,
	failure_reason   text NOT NULL DEFAULT '',
	created_at       timestamptz NOT NULL DEFAULT now(),
	updated_at       timestamptz NOT NULL DEFAULT now(),
	UNIQUE (message_id, recipient_role)
);

CREATE INDEX IF NOT EXISTS chat_message_addressees_msg_idx
	ON chat_message_addressees (message_id, id);

CREATE INDEX IF NOT EXISTS chat_message_addressees_proj_role_idx
	ON chat_message_addressees (project_id, recipient_role, delivery_state);

ALTER TABLE spec_decisions
	ADD COLUMN IF NOT EXISTS prompt_key text NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS prompt_version integer NOT NULL DEFAULT 1 CHECK (prompt_version >= 1),
	ADD COLUMN IF NOT EXISTS provenance text NOT NULL DEFAULT 'direct' CHECK (provenance IN ('direct', 'agent_interpreted')),
	ADD COLUMN IF NOT EXISTS source_message_id bigint REFERENCES chat_messages(id) ON DELETE SET NULL,
	ADD COLUMN IF NOT EXISTS superseded_by integer;
