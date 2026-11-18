-- M2: Setup and credentials (design 12.6, 13.3).

-- A sandbox's token, as its hash only. A run's token stays in run_tokens; a sandbox without a run
-- (Setup, a login check) gets one here. The broker answers a request only with a token of the
-- sandbox whose network it comes from (12.6).
CREATE TABLE sandbox_tokens (
    sha256      text PRIMARY KEY,
    sandbox     text        NOT NULL REFERENCES sandboxes (id),
    ends_at     timestamptz NOT NULL
);
CREATE INDEX sandbox_tokens_by_sandbox ON sandbox_tokens (sandbox);

-- What a sandbox may have from the broker, fixed when it starts (12.6): the kind says which
-- credentials, cli names the one CLI whose login it gets, and repos the repositories its
-- temporary GitHub token may read.
ALTER TABLE sandboxes
    ADD COLUMN cli      text,
    ADD COLUMN repos    text[]  NOT NULL DEFAULT '{}',
    ADD COLUMN gcloud   boolean NOT NULL DEFAULT false;

-- The state of each CLI's login, as the login check last saw it (12.6). One row per CLI.
CREATE TABLE cli_logins (
    cli         text PRIMARY KEY,
    state       text        NOT NULL DEFAULT 'unknown' CHECK (state IN ('unknown', 'signed_in', 'signed_out')),
    failures    integer     NOT NULL DEFAULT 0,
    checked_at  timestamptz,
    result      text        NOT NULL DEFAULT '',
    real_at     timestamptz,
    version     text,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
