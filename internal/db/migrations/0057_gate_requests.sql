-- Design 0.52 (M16, P4). A gate's request to the updater (16.2 step 5).
-- older versions read: yes
--
-- The updater's gate is a request of its own, `gate`, which the server copies here as every request.
-- `check` stays for the requests of an updater before 0.52. An older version reads the row as text.
ALTER TABLE update_requests DROP CONSTRAINT update_requests_action_check;
ALTER TABLE update_requests ADD CONSTRAINT update_requests_action_check CHECK (action IN ('install', 'rollback', 'check', 'gate'));
