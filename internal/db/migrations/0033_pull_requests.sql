-- Design 0.43, M10: a pull request per ticket (7.12). A ticket's branch is pushed to the remote from
-- the quarantine repository, where its runs pushed it; main is pushed from the canonical one, as
-- before. What Crew tells GitHub about a pull request, opening it, its comments, its statuses, and
-- closing it, waits in a queue of its own and is tried again, as a push is, so GitHub that cannot be
-- reached holds up nothing.
ALTER TABLE pushes ADD COLUMN source text NOT NULL DEFAULT 'canonical' CHECK (source IN ('canonical', 'quarantine'));
ALTER TABLE pushes ADD COLUMN ticket bigint REFERENCES tickets (id);

CREATE TABLE pull_requests (
    ticket      bigint      PRIMARY KEY REFERENCES tickets (id),
    project     uuid        NOT NULL REFERENCES projects (id),
    repo        text        NOT NULL,
    branch      text        NOT NULL,
    number      integer,
    url         text,
    -- opening until GitHub answered; merged when Crew merged the ticket, closed when it was closed
    state       text        NOT NULL DEFAULT 'opening' CHECK (state IN ('opening', 'open', 'merged', 'closed')),
    -- the head of the branch Crew last queued to push, and a head held back because it changes files
    -- that are code and the operator has not allowed it (13.8)
    pushed      text,
    held        text,
    opened_at   timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pull_requests_by_project ON pull_requests (project);

CREATE TABLE github_posts (
    id          bigserial PRIMARY KEY,
    project     uuid        NOT NULL REFERENCES projects (id),
    ticket      bigint      NOT NULL REFERENCES tickets (id),
    kind        text        NOT NULL CHECK (kind IN ('open', 'comment', 'status', 'close')),
    body        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    state       text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done')),
    tries       integer     NOT NULL DEFAULT 0,
    detail      text        NOT NULL DEFAULT '',
    not_before  timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    done_at     timestamptz
);
CREATE INDEX github_posts_pending ON github_posts (not_before) WHERE state = 'pending';
