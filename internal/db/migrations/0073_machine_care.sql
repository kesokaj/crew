-- Design 0.67 (M17, P5). Looking after the machine: the restart for security updates, and the news of
-- it and of a certificate that runs out (16.10).
-- older versions read: yes
--
-- crew-ctl asks the server every 15 minutes whether the machine may restart (crew admin
-- restart-check). reboot_wanted_at is when it first said a restart is needed; restart_wait is when
-- seven days of waiting passed, and holds new runs until the machine restarted; restart_at is when the
-- server last said yes, and holds new runs for ten minutes, so nothing starts while the machine goes
-- down. machine_news is news on the Crew level that no other row says: each line once, by its key. An
-- older version reads none of it, so it holds nothing for a restart and shows no such news.
ALTER TABLE crew_state
    ADD COLUMN reboot_wanted_at timestamptz,
    ADD COLUMN restart_wait     timestamptz,
    ADD COLUMN restart_at       timestamptz;

CREATE TABLE machine_news (
    id   bigserial   PRIMARY KEY,
    at   timestamptz NOT NULL DEFAULT now(),
    kind text        NOT NULL CHECK (kind IN ('restart', 'restart_wait', 'certificate')),
    key  text        NOT NULL UNIQUE CHECK (length(key) BETWEEN 1 AND 200),
    text text        NOT NULL CHECK (length(text) BETWEEN 1 AND 1000)
);
CREATE INDEX machine_news_at ON machine_news (at);
