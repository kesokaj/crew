-- Design 0.75 (M20, phase 1). Conversations with the operator and the chat of a ticket (11.8, 11.9,
-- 12.10).
-- older versions read: yes
--
-- A conversation's purpose is a run's (`run`), the proof's (`proof`, crew-ctl's), the operator's (`operator`, about one project or none),
-- or a ticket's chat (`chat`, one per ticket). Its turns are runs of one subtask of the scope
-- conversation, which it names. The operator's messages wait in conversation_messages until a run
-- takes them, in the transaction that records the run, as a draft's do. The work folder of a
-- conversation is kept as a gzip tar, at most 200 MB of files. A ticket a conversation's agent made
-- names the conversation and the run; the operator adopts it. A comment the operator sends on with
-- marked text keeps the quote and where it came from. A plan draft may come from a conversation. An
-- older version reads none of the new columns and lists conversations of no purpose of its own.
ALTER TABLE conversations
    ADD COLUMN title       text,
    ADD COLUMN closed_at   timestamptz,
    ADD COLUMN pick_model  text,
    ADD COLUMN pick_effort text,
    ADD COLUMN subtask     bigint REFERENCES subtasks (id) ON DELETE SET NULL,
    ADD CONSTRAINT conversations_purpose_check CHECK (purpose IN ('run', 'proof', 'operator', 'chat')),
    ADD CONSTRAINT conversations_operator_check CHECK (purpose <> 'operator' OR (ticket IS NULL AND draft IS NULL)),
    ADD CONSTRAINT conversations_chat_check CHECK (purpose <> 'chat' OR (project IS NOT NULL AND ticket IS NOT NULL AND draft IS NULL));
CREATE UNIQUE INDEX conversations_one_chat ON conversations (ticket) WHERE purpose = 'chat';
CREATE INDEX conversations_operator ON conversations (created_at) WHERE purpose = 'operator';

CREATE TABLE conversation_messages (
    id           bigserial PRIMARY KEY,
    conversation uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    text         text        NOT NULL CHECK (length(text) <= 20000),
    quote        text        CHECK (length(quote) <= 20000),
    source       jsonb,
    run          uuid REFERENCES runs (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (text <> '' OR coalesce(quote, '') <> '')
);
CREATE INDEX conversation_messages_waiting ON conversation_messages (conversation, id) WHERE run IS NULL;

-- gzip is sealed with the key file for its conversation (plan M20 D41), so the files the agent wrote are
-- not readable in the database or a backup; sha256 is of the tar, to see whether it changed.
CREATE TABLE work_folders (
    conversation uuid        PRIMARY KEY REFERENCES conversations (id) ON DELETE CASCADE,
    gzip         bytea       NOT NULL,
    sha256       text        NOT NULL,
    bytes        bigint      NOT NULL CHECK (bytes >= 0 AND bytes <= 209715200),
    files        integer     NOT NULL CHECK (files >= 0 AND files <= 20000),
    saved_at     timestamptz NOT NULL DEFAULT now(),
    turn         integer,
    note         text
);

ALTER TABLE tickets
    ADD COLUMN conversation     uuid REFERENCES conversations (id) ON DELETE SET NULL,
    ADD COLUMN conversation_run uuid,
    ADD COLUMN adopted_at       timestamptz;
CREATE INDEX tickets_by_conversation ON tickets (conversation) WHERE conversation IS NOT NULL;

ALTER TABLE records
    ADD COLUMN quote        text CHECK (length(quote) <= 20000),
    ADD COLUMN quote_source jsonb;

ALTER TABLE plan_drafts ADD COLUMN source_conversation uuid REFERENCES conversations (id) ON DELETE SET NULL;

-- The commit a read-only run reads in place of its branch's head: a chat's of a Done ticket reads main
-- at the ticket's merge commit (11.9).
ALTER TABLE runs ADD COLUMN git_commit text CHECK (git_commit ~ '^[0-9a-f]{40}$');

-- The operator deletes a conversation of their own, about a project or none, with everything of it:
-- its messages and work folder (by cascade), its CLI files, its turns, the events of it and of its runs,
-- its runs with their prompts, tokens and spools, its subtask, and the rows of its ended sandboxes
-- (11.8, plan M20 D22). A record, a decision or another row elsewhere that names one of its runs keeps
-- its row and forgets the run. A conversation whose run has not ended is not deleted: Close first.
CREATE FUNCTION crew_delete_conversation(c uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    cpurpose text;
BEGIN
    SELECT purpose INTO cpurpose FROM conversations WHERE id = c FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no conversation %', c;
    END IF;
    IF cpurpose <> 'operator' THEN
        RAISE EXCEPTION 'conversation % is a %''s: only the operator''s own are deleted', c, cpurpose;
    END IF;
    CREATE TEMP TABLE pg_temp.delc_s ON COMMIT DROP AS SELECT id FROM subtasks
        WHERE scope = 'conversation' AND (id = (SELECT subtask FROM conversations WHERE id = c)
            OR id IN (SELECT subtask FROM runs WHERE conversation = c));
    CREATE TEMP TABLE pg_temp.delc_r ON COMMIT DROP AS SELECT id FROM runs
        WHERE conversation = c OR subtask IN (SELECT id FROM pg_temp.delc_s);
    IF EXISTS (SELECT 1 FROM runs WHERE id IN (SELECT id FROM pg_temp.delc_r) AND state <> 'ended') THEN
        RAISE EXCEPTION 'conversation % has a run that has not ended: close it first', c;
    END IF;
    CREATE TEMP TABLE pg_temp.delc_b ON COMMIT DROP AS SELECT sandbox AS id FROM runs
        WHERE id IN (SELECT id FROM pg_temp.delc_r) AND sandbox IS NOT NULL;

    UPDATE records SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE records SET delivered_run = NULL WHERE delivered_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE records SET subtask = NULL WHERE subtask IN (SELECT id FROM pg_temp.delc_s);
    UPDATE stage_moves SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE stage_moves SET subtask = NULL WHERE subtask IN (SELECT id FROM pg_temp.delc_s);
    UPDATE decisions SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE memory SET source_run = NULL WHERE source_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE bugs SET found_run = NULL WHERE found_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE feedback SET from_run = NULL WHERE from_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE skill_requests SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE eval_cases SET proposed_run = NULL WHERE proposed_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE overrides SET proposed_run = NULL WHERE proposed_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE improvements SET proposed_run = NULL WHERE proposed_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE retrospectives SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE reports SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE plan_draft_versions SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE draft_messages SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE prompt_rewrites SET run = NULL WHERE run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE eval_runs SET builder_run = NULL WHERE builder_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE eval_runs SET reviewer_run = NULL WHERE reviewer_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE eval_runs SET tester_run = NULL WHERE tester_run IN (SELECT id FROM pg_temp.delc_r);
    UPDATE subtasks SET summarises = NULL WHERE summarises = c;
    UPDATE runs SET continues = NULL WHERE continues IN (SELECT id FROM pg_temp.delc_r) AND id NOT IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM summaries WHERE subtask IN (SELECT id FROM pg_temp.delc_s);
    DELETE FROM signals WHERE run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM mcp_calls WHERE run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM ref_pushes WHERE run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM run_prompts WHERE run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM run_spools WHERE run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM run_tokens WHERE run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM terminal_log WHERE run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM turns WHERE conversation = c OR run IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM events WHERE run IN (SELECT id FROM pg_temp.delc_r) OR conversation = c;
    DELETE FROM cli_files WHERE conversation = c;
    DELETE FROM subtask_steps WHERE subtask IN (SELECT id FROM pg_temp.delc_s);
    UPDATE conversations SET subtask = NULL WHERE id = c;
    UPDATE runs SET continues = NULL WHERE id IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM runs WHERE id IN (SELECT id FROM pg_temp.delc_r);
    DELETE FROM subtasks WHERE id IN (SELECT id FROM pg_temp.delc_s);
    DELETE FROM sandbox_tokens WHERE sandbox IN (SELECT id FROM pg_temp.delc_b);
    DELETE FROM sandboxes WHERE id IN (SELECT id FROM pg_temp.delc_b) AND removed_at IS NOT NULL;
    DELETE FROM conversations WHERE id = c;
END $$;
REVOKE ALL ON FUNCTION crew_delete_conversation(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION crew_delete_conversation(uuid) TO crew;
