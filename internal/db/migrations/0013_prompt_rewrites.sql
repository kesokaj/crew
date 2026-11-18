-- M7: the prompt writer (design 7.2). Every rewrite of what the operator wrote: the first message of a
-- plan draft, which gets a proposal by itself, and Improve text and Review text on a selection. Both
-- texts, what the prompt writer said, the operator's choice, and the text that was sent or used are
-- kept as long as the draft or the project. No agent of a project reads them.
CREATE TABLE prompt_rewrites (
    id          bigserial PRIMARY KEY,
    kind        text        NOT NULL CHECK (kind IN ('first', 'improve', 'review')),
    draft       uuid REFERENCES plan_drafts (id),
    project     uuid REFERENCES projects (id),
    run         uuid REFERENCES runs (id),
    -- Rewrite again: the rewrite before, and which round this is (at most three).
    previous    bigint REFERENCES prompt_rewrites (id),
    round       integer     NOT NULL DEFAULT 1 CHECK (round BETWEEN 1 AND 3),
    original    text        NOT NULL,
    answers     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    -- writing, then ready (a rewrite or notes), good (good as it is), late, failed, or off
    state       text        NOT NULL DEFAULT 'writing' CHECK (state IN ('writing', 'ready', 'good', 'late', 'failed', 'off')),
    rewrite     text,
    changes     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    assumptions jsonb       NOT NULL DEFAULT '[]'::jsonb,
    questions   jsonb       NOT NULL DEFAULT '[]'::jsonb,
    notes       jsonb       NOT NULL DEFAULT '[]'::jsonb,
    detail      text,
    -- the operator's choice: for a first message the rewrite, an edited rewrite, or their own; for
    -- Improve text use it, an edit of it, or keep theirs; for Review text, seen
    choice      text CHECK (choice IN ('rewrite', 'edited', 'mine', 'use', 'keep', 'seen')),
    sent        text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    chosen_at   timestamptz,
    CHECK (draft IS NOT NULL OR project IS NOT NULL)
);
CREATE INDEX prompt_rewrites_by_project ON prompt_rewrites (project, id) WHERE project IS NOT NULL;
CREATE INDEX prompt_rewrites_by_draft ON prompt_rewrites (draft, id) WHERE draft IS NOT NULL;

-- A message of the operator in a draft names the rewrite it was chosen from, so the draft shows which
-- text was sent and the original is one click away; and a request about a part of the plan holds the
-- part, which reaches the planning agent fenced as data (7.2).
ALTER TABLE draft_messages
    ADD COLUMN rewrite bigint REFERENCES prompt_rewrites (id),
    ADD COLUMN quote   text,
    ADD COLUMN ask     text CHECK (ask IN ('improve', 'review'));
