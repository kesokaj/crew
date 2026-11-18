-- Design 0.71 (M18, phase 1). Notifications by mail (18).
-- older versions read: yes
--
-- notifications is every message Crew sent or will send, kept as it looked (18.1): one needs_you for
-- each episode of an inbox item, from the pass that first lists its id to the first pass that no
-- longer does (ended_at), so an item whose Since moves is not mailed again; one reminder for each
-- episode; news by its key, once; one digest a day; a test message for each press. inbox_decisions is
-- each press of an inbox alternative that its action took, so an old link shows what was decided and
-- when (18.3). notify_state is one row: since when the three kinds sent at once are read, where the
-- daily mail stands, and the mail login's last success and refusal. An older version reads none of
-- them, so it sends no mail and its inbox says nothing of one.
CREATE TABLE notifications (
    id         bigserial   PRIMARY KEY,
    kind       text        NOT NULL CHECK (kind IN ('needs_you', 'reminder', 'news', 'digest', 'test')),
    item       text        CHECK (length(item) BETWEEN 1 AND 300),
    episode    bigint      REFERENCES notifications (id),
    ended_at   timestamptz,
    project    text        NOT NULL DEFAULT '',
    subject    text        NOT NULL CHECK (length(subject) BETWEEN 1 AND 300),
    body       text        NOT NULL CHECK (length(body) <= 20000),
    links      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    state      text        NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'sending', 'sent', 'failed', 'dropped')),
    attempts   integer     NOT NULL DEFAULT 0,
    next_at    timestamptz NOT NULL DEFAULT now(),
    error      text        CHECK (length(error) <= 400),
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz,
    CHECK ((kind = 'test') = (item IS NULL)),
    CHECK ((kind = 'reminder') = (episode IS NOT NULL))
);
CREATE UNIQUE INDEX notifications_one_open_episode ON notifications (item) WHERE kind = 'needs_you' AND ended_at IS NULL;
CREATE UNIQUE INDEX notifications_one_reminder ON notifications (episode) WHERE kind = 'reminder';
CREATE UNIQUE INDEX notifications_one_per_key ON notifications (item) WHERE kind IN ('news', 'digest');
CREATE INDEX notifications_by_state ON notifications (state, next_at);
CREATE INDEX notifications_by_item ON notifications (item, id);

CREATE TABLE inbox_decisions (
    id       bigserial   PRIMARY KEY,
    item     text        NOT NULL CHECK (length(item) BETWEEN 1 AND 300),
    label    text        NOT NULL CHECK (length(label) BETWEEN 1 AND 100),
    by_email text        NOT NULL,
    at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX inbox_decisions_by_item ON inbox_decisions (item, at);

CREATE TABLE notify_state (
    one              boolean     PRIMARY KEY DEFAULT true CHECK (one),
    news_from        timestamptz NOT NULL DEFAULT now(),
    digest_from      timestamptz NOT NULL DEFAULT now(),
    login_ok_at      timestamptz,
    login_refused_at timestamptz,
    login_error      text        CHECK (length(login_error) <= 400)
);
INSERT INTO notify_state DEFAULT VALUES;
