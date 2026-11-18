-- Design 0.79. No run and no conversation gets gcloud (AGENTS.md safety rule 8, 12.6), so the settings
-- that left it out, a project's sandbox.credentials and the machine's conversations.credentials, are
-- no settings any more (14.1). They are taken out of the stored settings, as step 62 took the budgets.
-- A sandbox of a kind other than a terminal is marked as holding no gcloud, so an older version after
-- a roll back gives none to one that runs on.
-- older versions read: yes
--
-- An older version fills a missing key from its own defaults; as built it gives no run gcloud.
UPDATE settings SET doc = doc #- '{sandbox,credentials}' WHERE doc #> '{sandbox,credentials}' IS NOT NULL;
UPDATE settings SET doc = doc #- '{conversations,credentials}' WHERE doc #> '{conversations,credentials}' IS NOT NULL;
UPDATE sandboxes SET gcloud = false WHERE gcloud AND kind <> 'terminal';
