-- Design 0.56. The daily look's tickets and its looks (12.8).
-- older versions read: yes
--
-- The operator chose (twelfth session, answer 8) that a tool and version gets one ticket, ever: a key of
-- the look's, such as go@1.27.4 or image:GO_IMAGE@sha256:…, is written in the transaction that adds its
-- ticket, and a key that is there already rolls that ticket back. The key is per project, so a Crew's
-- project made again starts with none; the versions.lock keys its ticket changes are kept with it, so
-- the look makes no ticket while an open one of it names one of those keys. Each look is kept with what
-- it read of each source, in Crew's own words, for Versions; the look keeps the newest 30. An older
-- version has no look and reads neither table.
CREATE TABLE pin_tickets (
    project   uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key       text        NOT NULL CHECK (length(key) BETWEEN 1 AND 200),
    ticket    bigint      NOT NULL REFERENCES tickets (id) ON DELETE CASCADE,
    lock_keys text[]      NOT NULL,
    made_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project, key)
);
CREATE INDEX pin_tickets_ticket ON pin_tickets (ticket);

CREATE TABLE pin_looks (
    id    bigserial   PRIMARY KEY,
    at    timestamptz NOT NULL DEFAULT now(),
    lines jsonb       NOT NULL,
    made  text[]      NOT NULL DEFAULT '{}'
);
