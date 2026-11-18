-- Design 0.57 (M16, P7). Every release measured (10.7).
-- older versions read: yes
--
-- Each release that was installed is compared, once, with the release it replaced at its first
-- install, on the tickets done wholly on each (the operator's answers 5 to 8 of the thirteenth
-- session): the result is kept on its row, Versions shows it, and a worse one is a line of news, read
-- from the row, so it comes once. A release that stopped running before this step is measured and
-- shown like the others, and makes no news: its past is not today's news. An older version reads none
-- of the three columns.
ALTER TABLE releases
    ADD COLUMN measure      jsonb,
    ADD COLUMN measured_at  timestamptz,
    ADD COLUMN measure_news boolean NOT NULL DEFAULT true;
UPDATE releases SET measure_news = false WHERE installed_at IS NOT NULL AND state <> 'running';
